package gateway

import (
	"context"
	"fmt"
	"log/slog"
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
func (g *Gateway) WatchdogLoop(ctx context.Context) {
	cfg := g.Cfg
	interval := time.Duration(cfg.HealthInterval) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}
	for {
		g.watchOnce()
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func (g *Gateway) watchOnce() {
	cfg := g.Cfg
	g.refreshSerials() // USB 在线设备补取硬件序列号（缓存过就秒回）
	for i := range cfg.Devices {
		dev := &cfg.Devices[i]
		if dev.UDID == "" || dev.IP == "" {
			continue
		}
		h := CheckWDA(dev.IP, dev.Port, 3*time.Second)
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
		if g.Exec.IsBusy(dev.UDID) {
			// 任务执行中周期性刷新 busy，避免平台 10 分钟 busyTTL 把忙碌设备误判为离线。
			g.Exec.status(DeviceStatus{UDID: dev.UDID, WDAStatus: "busy", Error: ""})
			g.rememberCloudStatus(dev.UDID, "busy")
		} else {
			// 非忙碌：云状态（含 WDA 进程退出/拉起）变化才上报，避免无意义刷屏。
			g.reportCloudStatusIfChanged(dev, usbConnected(dev.UDID), errText(h.Error))
		}
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

// autoAssignIP 对「已激活但未配置 IP」的设备自动探测局域网 WDA。
func (g *Gateway) autoAssignIP() error {
	cfg := g.Cfg
	known := map[string]bool{}
	for _, d := range cfg.Devices {
		if d.IP != "" {
			known[d.IP] = true
		}
	}
	// 未配置 IP 且 WDA 已运行的设备
	var pending []string
	for _, d := range cfg.Devices {
		if d.IP == "" && g.WDA.Running(d.UDID) {
			pending = append(pending, d.UDID)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	found := ScanLANWDA(500 * time.Millisecond)
	var cands []FoundWDA
	for _, f := range found {
		if !known[f.IP] && f.IP != "127.0.0.1" {
			cands = append(cands, f)
		}
	}
	if len(pending) == 1 && len(cands) == 1 {
		if d := cfg.Device(pending[0]); d != nil {
			d.IP = cands[0].IP
			d.Port = 8100
			d.AutoReactivate = true
			_ = cfg.Save()
			slog.Info("auto-assigned WDA IP", "udid", pending[0][:8], "ip", cands[0].IP)
		}
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
