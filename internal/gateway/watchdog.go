package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"wda-farm-gateway/internal/wda"
)

const wdaStartGrace = 90 * time.Second

// usbConnected 判断设备当前是否 USB 直连（ioreg UsbAppleDeviceUDID）。
// 网络不可达且未 USB 连接 = 设备物理离线（拔线/断电/Wi-Fi 断开），
// 跳过 xcodebuild 重激活，避免看护循环对离线设备反复构建失败（P2-10 根因修复）。
func usbConnected(udid string) bool {
	for _, u := range USBUDIDs() {
		if strings.EqualFold(u, udid) {
			return true
		}
	}
	return false
}

// reactivateDecision 是否应重激活：健康失败 + 允许自动重激活 + 未在运行 + USB 在线。
func reactivateDecision(healthy, autoReactivate, running, usbAttached bool) bool {
	return !healthy && autoReactivate && !running && usbAttached
}

// WatchdogLoop 逐台健康检查、自动重激活、网络跟随、自动配 IP。
// 每轮带 panic 防护：单轮异常只跳过该轮并留痕，看护循环本身不能死。
func (g *Gateway) WatchdogLoop(ctx context.Context) {
	cfg := g.Cfg
	interval := time.Duration(cfg.HealthInterval) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}
	for {
		g.watchOnceSafe()
		select {
		case <-ctx.Done():
			return
		case <-g.kickWatchdog: // 激活等事件触发：立即进入下一轮（IP 自动发现不等周期）
		case <-time.After(interval):
		}
	}
}

func (g *Gateway) watchOnceSafe() {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("watchdog round panicked", "panic", r)
		}
	}()
	g.watchOnce()
}

func (g *Gateway) watchOnce() {
	cfg := g.Cfg
	g.refreshSerials() // USB 在线设备补取硬件序列号（缓存过就秒回）
	// USB 隧道对账：配置内且 USB 在线的设备起本地隧道，拔线即撤。
	tunnelPorts := map[string]int{}
	for _, d := range cfg.Devices {
		if d.UDID != "" && d.Port != 0 {
			tunnelPorts[d.UDID] = d.Port
		}
	}
	EnsureUSBTunnels(tunnelPorts)
	// easytier 看护自愈：用户未主动停止时，进程崩溃自动拉回（限速）。
	if g.EasyTier != nil {
		g.EasyTier.Supervise()
	}
	for i := range cfg.Devices {
		dev := &cfg.Devices[i]
		if dev.UDID == "" || dev.IP == "" {
			continue
		}
		// 任务执行中 WDA 被会话/元素请求占满，/status 探活易超时误判离线：
		// 保留上一轮健康状态，仅刷新 busy（平台 10 分钟 busyTTL 内不误判离线）。
		if g.Exec.IsBusy(dev.UDID) {
			g.Exec.status(DeviceStatus{UDID: dev.UDID, WDAStatus: "busy", Error: ""})
			g.rememberCloudStatus(dev.UDID, "busy")
			continue
		}
		h := g.checkWDA(dev)
		prevOK := healthOK(dev.LastHealth)
		applyHealth(dev, h)
		// 在线时记录 WDA identifierForVendor(uuid) 与设备名（如 iPhone Plus-2），供识别与网络变化后按 uuid 重新匹配。
		if h.OK && (dev.VendorUUID == "" || dev.Name == "") {
			uuid, name := g.deviceIdentity(dev.IP, dev.Port)
			dirty := false
			if dev.VendorUUID == "" && uuid != "" {
				dev.VendorUUID = uuid
				dirty = true
				slog.Info("device recorded vendor_uuid", "udid", dev.UDID[:8], "uuid", uuid)
			}
			if dev.Name == "" && name != "" {
				dev.Name = name
				dirty = true
			}
			if dirty {
				_ = cfg.Save()
			}
		}
		// 非忙碌：云状态（含 WDA 进程退出/拉起）变化才上报，避免无意义刷屏。
		g.reportCloudStatusIfChanged(dev, usbConnected(dev.UDID), errText(h.Error))
		if !h.OK && dev.AutoReactivate && !g.WDA.Running(dev.UDID) {
			if !usbConnected(dev.UDID) {
				// 设备物理离线（无网络且未接 USB）：跳过重激活，避免 xcodebuild 反复失败。
				if prevOK {
					slog.Warn("device offline and not USB-attached, skip reactivation",
						"udid", dev.UDID[:8], "ip", dev.IP)
				}
				if h.Error == "" || !strings.Contains(h.Error, "USB") {
					dev.LastHealth["error"] = "device offline: network unreachable and not USB-attached; skipped reactivation (reconnect USB/Wi-Fi)"
				}
				continue
			}
			// 复核：瞬时抖动（USB 隧道断/网络闪断）会让探活误判离线，此时拉起
			// xcodebuild 会与手机端仍在运行的 WDA 抢占冲突（exit 65 反复失败）。
			// 再探一次，健康则跳过重激活，沿用手机端存活的 WDA。
			if h2 := g.checkWDA(dev); h2.OK {
				applyHealth(dev, h2)
				slog.Info("WDA healthy on recheck, skip reactivation", "udid", dev.UDID[:8], "ip", dev.IP)
				continue
			}
			slog.Info("WDA down, reactivating", "udid", dev.UDID[:8], "ip", dev.IP)
			if err := g.WDA.Activate(dev.UDID, dev.Port, dev.UDID); err != nil {
				slog.Error("reactivate failed", "udid", dev.UDID[:8], "error", err)
			}
		}
	}
	_ = g.autoAssignIP()
	_ = g.followNetworkChange()
	_ = cfg.Save() // 每轮探活后持久化 last_health，网关重启后不再用过期状态上报
}

// checkWDA 探测设备 WDA 健康：USB 隧道优先（不依赖手机 Wi-Fi），
// 无隧道或隧道不通时回退 Wi-Fi IP。
func (g *Gateway) checkWDA(dev *Device) WDAHealth {
	if a := TunnelAddr(dev.UDID); a != "" {
		host, portStr, err := net.SplitHostPort(a)
		if err == nil {
			p, _ := strconv.Atoi(portStr)
			h := CheckWDA(host, p, 3*time.Second)
			if h.OK {
				return h
			}
			slog.Warn("usb tunnel health failed, fallback to wifi", "udid", dev.UDID[:8], "error", h.Error)
		}
	}
	return CheckWDA(dev.IP, dev.Port, 3*time.Second)
}

// deviceIdentity 读取 WDA identifierForVendor(uuid) 与设备名（健康探活通过后调用）。
func (g *Gateway) deviceIdentity(ip string, port int) (uuid, name string) {
	if port == 0 {
		port = 8100
	}
	client := wda.NewClient(fmt.Sprintf("http://%s:%d", ip, port), 3*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	uuid, name, _, err := client.DeviceInfo(ctx)
	if err != nil {
		return "", ""
	}
	return uuid, name
}

// pendingIPDev 是一台待分配 IP 的设备（已激活、WDA 运行中、无 IP）。
type pendingIPDev struct {
	udid, vendorUUID, selfIP string // selfIP=经 USB 隧道问 WDA /status 得到的手机自报 Wi-Fi IP
}

// decideIPAssignments 计算待分配设备的 IP（纯逻辑，可单测）。分配优先级：
//  1. 隧道自报 IP：私网、未被其它设备占用 → 直接采纳（手机自述地址，无认错风险）；
//  2. 扫描候选按 vendor_uuid 强匹配认领（候选须无主）；
//  3. 剩余恰好 1 台待分配且恰有 1 个无主候选 → 唯一性分配（多候选时仍不猜，防认错手机）。
func decideIPAssignments(pending []pendingIPDev, found []FoundWDA, owner map[string]string) map[string]string {
	res := map[string]string{}
	claimed := map[string]bool{} // 本轮已认领的候选 IP
	unownedCands := 0
	for _, f := range found {
		if f.IP != "" && f.IP != "127.0.0.1" {
			if _, taken := owner[f.IP]; !taken {
				unownedCands++
			}
		}
	}
	// 1) 隧道自报
	var rest []pendingIPDev
	for _, p := range pending {
		ip := p.selfIP
		if p4 := net.ParseIP(ip); ip != "" && p4 != nil && p4.To4() != nil && isPrivateIPv4(p4.To4()) {
			if _, taken := owner[ip]; !taken && !claimed[ip] {
				res[p.udid] = ip
				claimed[ip] = true
				continue
			}
		}
		rest = append(rest, p)
	}
	// 2) vendor_uuid 强匹配
	var left []pendingIPDev
	for _, p := range rest {
		matched := false
		if p.vendorUUID != "" {
			for _, f := range found {
				if f.UUID == p.vendorUUID && f.IP != "" && f.IP != "127.0.0.1" && !claimed[f.IP] {
					if _, taken := owner[f.IP]; !taken {
						res[p.udid] = f.IP
						claimed[f.IP] = true
						matched = true
						break
					}
				}
			}
		}
		if !matched {
			left = append(left, p)
		}
	}
	// 3) 唯一性规则：恰好 1 台待分配 且 恰好 1 个无主候选
	if len(left) == 1 && unownedCands == 1 {
		for _, f := range found {
			if f.IP == "" || f.IP == "127.0.0.1" || claimed[f.IP] {
				continue
			}
			if _, taken := owner[f.IP]; !taken {
				res[left[0].udid] = f.IP
				break
			}
		}
	}
	return res
}

// autoAssignIP 对「已激活但未配置 IP」的设备自动分配 Wi-Fi IP。
// 优先 USB 隧道直达（手机经 WDA 自报 IP，秒级完成、无认错风险，顺带记录 vendor_uuid/name）；
// 无隧道的设备走局域网扫描兜底：先物理网卡网段（快路径，避开 VPN/TUN 空网段），
// 无无主候选再全网段扫描。
func (g *Gateway) autoAssignIP() error {
	cfg := g.Cfg
	owner := map[string]string{} // ip -> udid：已配置设备的 IP 归属，防止被弱匹配抢占
	for _, d := range cfg.Devices {
		if d.UDID != "" && d.IP != "" {
			owner[d.IP] = d.UDID
		}
	}
	var pending []pendingIPDev
	dirty := false
	for i := range cfg.Devices {
		d := &cfg.Devices[i]
		if d.UDID == "" || d.IP != "" || !g.WDA.Running(d.UDID) {
			continue
		}
		p := pendingIPDev{udid: d.UDID, vendorUUID: d.VendorUUID}
		// USB 隧道在：经隧道记录设备身份，并取 WDA /status 的 ios.ip 作为自报 IP。
		// WDA 刚拉起可能还没监听：短暂重试，让激活后踢进来的一轮当场完成分配。
		if a := TunnelAddr(d.UDID); a != "" {
			if host, portStr, err := net.SplitHostPort(a); err == nil {
				port, _ := strconv.Atoi(portStr)
				var h WDAHealth
				for i := 0; i < 3; i++ {
					h = CheckWDA(host, port, 2*time.Second)
					if h.OK {
						break
					}
					time.Sleep(2 * time.Second)
				}
				if h.OK {
					if d.VendorUUID == "" || d.Name == "" {
						uuid, name := g.deviceIdentity(host, port)
						if d.VendorUUID == "" && uuid != "" {
							d.VendorUUID = uuid
							dirty = true
							slog.Info("device recorded vendor_uuid (tunnel)", "udid", d.UDID[:8], "uuid", uuid)
						}
						if d.Name == "" && name != "" {
							d.Name = name
							dirty = true
						}
					}
					if h.IP != "" {
						p.selfIP = h.IP
					}
				}
			}
		}
		pending = append(pending, p)
	}
	if dirty {
		_ = cfg.Save()
	}
	if len(pending) == 0 {
		return nil
	}

	assign := decideIPAssignments(pending, nil, owner)
	if len(assign) < len(pending) {
		// 还有未分配：扫描兜底。先物理网卡网段（快路径），无无主候选再全网段。
		found := scanSubnets(physicalSubnets(), 500*time.Millisecond)
		hasUnowned := false
		for _, f := range found {
			if f.IP != "" && f.IP != "127.0.0.1" {
				if _, taken := owner[f.IP]; !taken {
					hasUnowned = true
					break
				}
			}
		}
		if !hasUnowned {
			found = ScanLANWDA(500 * time.Millisecond)
		}
		for udid, ip := range decideIPAssignments(pending, found, owner) {
			assign[udid] = ip
		}
	}

	for udid, ip := range assign {
		if d := cfg.Device(udid); d != nil {
			d.IP = ip
			d.Port = 8100
			d.AutoReactivate = true
			dirty = true
			slog.Info("auto-assigned WDA IP", "udid", udid[:8], "ip", ip)
		}
	}
	if dirty {
		_ = cfg.Save()
	}
	return nil
}

// followNetworkChange Mac 换网后：IP 不可达的已配置设备按 uuid（或 ios版本+model）重新匹配到新 IP。
func (g *Gateway) followNetworkChange() error {
	cfg := g.Cfg
	var stale []*Device
	staleSet := map[string]bool{}
	for i := range cfg.Devices {
		d := &cfg.Devices[i]
		if d.UDID != "" && d.IP != "" && !healthOK(d.LastHealth) {
			stale = append(stale, d)
			staleSet[d.UDID] = true
		}
	}
	if len(stale) == 0 {
		return nil
	}
	found := ScanLANWDA(500 * time.Millisecond)
	if len(found) == 0 {
		return nil
	}
	// IP 归属守卫：iOS 版本+型号兜底匹配区分不了同型号同版本的机器，
	// 认错手机会让消息从错误的设备发出。已被其它设备配置的 IP 不允许被弱匹配抢占；
	// uuid 强匹配也只允许落在无主、或属主同样失联（真·换网互换）的 IP 上。
	owner := map[string]string{}
	for _, d := range cfg.Devices {
		if d.UDID != "" && d.IP != "" {
			owner[d.IP] = d.UDID
		}
	}
	weakFree := func(ip, udid string) bool {
		o, ok := owner[ip]
		return !ok || o == udid
	}
	uuidFree := func(ip, udid string) bool {
		o, ok := owner[ip]
		return !ok || o == udid || staleSet[o]
	}
	byUUID := map[string]FoundWDA{}
	for _, f := range found {
		if f.UUID != "" && uuidFree(f.IP, "") {
			byUUID[f.UUID] = f
		}
	}
	for _, dev := range stale {
		old := dev.IP
		hit, ok := byUUID[dev.VendorUUID]
		if !ok {
			var cands []FoundWDA
			for _, f := range found {
				if !weakFree(f.IP, dev.UDID) {
					continue
				}
				if f.IOSVersion != dev.IOSVersion {
					continue
				}
				if dev.Model != "" && f.Model != "" && f.Model != dev.Model {
					continue
				}
				cands = append(cands, f)
			}
			if len(cands) == 1 {
				hit = cands[0]
			} else if len(cands) > 1 {
				slog.Warn("new IP ambiguous, skip auto-follow", "udid", dev.UDID[:8], "candidates", len(cands))
				continue
			}
		}
		if hit.IP == "" || hit.IP == old {
			continue
		}
		dev.IP = hit.IP
		if hit.UUID != "" && dev.VendorUUID == "" {
			// 只在无 uuid 时记录；避免把别的手机的 uuid 覆盖进来污染设备身份
			dev.VendorUUID = hit.UUID
		}
		owner[hit.IP] = dev.UDID // 本轮内占用，后续设备不会再匹配到同一 IP
		// 换 IP 后先对新 IP 做一次真实探活，就绪才报 online；未就绪交给下一轮 watchOnce 修正。
		h := CheckWDA(dev.IP, dev.Port, 3*time.Second)
		applyHealth(dev, h)
		_ = cfg.Save()
		slog.Info("device followed network change", "udid", dev.UDID[:8], "old", old, "new", hit.IP, "ok", h.OK)
		if h.OK {
			g.Exec.status(DeviceStatus{UDID: dev.UDID, WDAStatus: "online", Error: "ip updated " + old + " -> " + hit.IP})
		}
	}
	return nil
}

// applyHealth 把一次 WDA 探活结果写入设备内存态（LastHealth + iOS 版本）。
func applyHealth(dev *Device, h WDAHealth) {
	dev.LastHealth = map[string]any{
		"ok": h.OK, "ready": h.Ready, "ip": h.IP,
		"ios_version": h.Version, "checked_at": float64(time.Now().Unix()),
		"starting": false, "error": h.Error,
	}
	if h.Version != "" {
		dev.IOSVersion = h.Version
	}
}

func healthOK(h map[string]any) bool {
	v, _ := h["ok"].(bool)
	return v
}

func errText(err string) string {
	if err == "" {
		return ""
	}
	return err
}
