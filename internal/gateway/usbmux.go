package gateway

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"
)

// USB / Network 隧道：iproxy 转发到手机 8100。
// 激活通道互斥：USB 激活只建 USB iproxy；Network 激活只建 iproxy -n。
// 拔线只拆除隧道进程，绝不 Stop 机上 WDA。

// usbTunnelProc 一台设备的 iproxy 进程。
type usbTunnelProc struct {
	cmd     *exec.Cmd
	port    int
	network bool // true=usbmux Network 隧道（iproxy -n，无线设备），false=USB 隧道
	done    chan struct{}
}

type usbTunnelManager struct {
	mu     sync.Mutex
	procs  map[string]*usbTunnelProc // udid -> iproxy 进程
	misses map[string]int            // udid 连续未在 USB 发现列表中的轮数（拆除防抖）
}

var usbTunnels = &usbTunnelManager{procs: map[string]*usbTunnelProc{}, misses: map[string]int{}}

// iproxyBin 定位 iproxy 可执行文件（PATH 优先，bundle 内置与 homebrew 兜底）。
// 打包交付形态：壳 App 把 bundle Resources/bin 注入 PATH，这里再显式兜底一次。
func iproxyBin() string {
	if _, err := exec.LookPath("iproxy"); err == nil {
		return "iproxy"
	}
	candidates := []string{}
	if res := os.Getenv("WDA_GATEWAY_RESOURCES"); res != "" {
		candidates = append(candidates, filepath.Join(res, "bin", "iproxy"))
	}
	candidates = append(candidates, "/opt/homebrew/bin/iproxy", "/usr/local/bin/iproxy")
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// freeLocalPort 取一个可用本地端口（listen :0 后立即释放，存在极小竞争窗口）。
// bundleLibFallback 打包形态下注入给 libimobiledevice 系子进程的 dyld 覆盖目录：
// 包内二进制保持 Homebrew 原样（install_name_tool 修改会致 dyld4 死循环），
// 其 LC_LOAD_DYLIB 是 Homebrew 绝对路径；DYLD_LIBRARY_PATH 优先于 LC 绝对路径按
// leaf name 命中（已实测 dyld 从包内 lib 加载），客户机有无 Homebrew 均确定走包内。
func bundleLibFallback() []string {
	if res := os.Getenv("WDA_GATEWAY_RESOURCES"); res != "" {
		return []string{"DYLD_LIBRARY_PATH=" + filepath.Join(res, "lib")}
	}
	return nil
}

// iproxyForwardArgs Mac/Windows 同一套通道选择；仅端口参数格式不同。
// Network 通道带 -n，不因 Windows 就改走 USB 隧道。
func iproxyForwardArgs(udid string, local, device int, network bool) []string {
	args := []string{"-u", udid}
	if network {
		args = append(args, "-n")
	}
	if runtime.GOOS == "windows" {
		return append(args, strconv.Itoa(local), strconv.Itoa(device))
	}
	return append(args, fmt.Sprintf("%d:%d", local, device))
}

func freeLocalPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// usbTunnelsToDrop 连续 missLimit 轮不在对应在线集合（USB 隧道看 usb，Network 隧道看 netSet）
// 里的隧道才拆除。枚举整表为空时也累计 miss，避免拔到另一台电脑后死隧道永不回收。
func usbTunnelsToDrop(usb, netSet map[string]bool, procs map[string]*usbTunnelProc, misses map[string]int, missLimit int) []string {
	if missLimit < 1 {
		missLimit = 1
	}
	var drop []string
	for udid, proc := range procs {
		set := usb
		if proc != nil && proc.network {
			set = netSet
		}
		if set[udidKey(udid)] {
			delete(misses, udid)
			continue
		}
		misses[udid]++
		if misses[udid] >= missLimit {
			drop = append(drop, udid)
			delete(misses, udid)
		}
	}
	return drop
}

// EnsureUSBTunnels 按当前 USB / usbmux Network 设备与目标端口对账隧道（看护循环每轮调用）。
// 拔线只停 iproxy，不触碰 WDA 激活进程 / 机上 XCTest。
//   - USB 直连设备：iproxy 走 USB（默认）；
//   - 无线设备（usbmux ConnectionType=Network、无 USB）：ios forward 走 Network 隧道
//     （经 netmuxd 转发到手机 Wi-Fi），让网关能访问 WDA 的 loopback :8100。
//     Windows 上不用 iproxy -n：libimobiledevice 不读 USBMUXD_SOCKET_ADDRESS，
//     连 AMDS 看不到 Network 条目，隧道建不起来；go-ios forward 读该环境变量。
//
// ports 为需要隧道的 udid -> 设备 WDA 端口（来自 devices.json）。
func EnsureUSBTunnels(rawPorts map[string]int, vias map[string]string) {
	udids := USBUDIDs()
	m := usbTunnels
	m.mu.Lock()
	defer m.mu.Unlock()
	// UDID 大小写归一化：iOS 16+ 新格式（8-16 hex 带连字符）在 iproxy 里大小写敏感
	// （小写连不上），统一经 normalizeUDID（8-16 转大写）保证与 discover 对账一致。
	ports := make(map[string]int, len(rawPorts))
	for u, p := range rawPorts {
		ports[normalizeUDID(u)] = p
	}
	viaOf := map[string]string{}
	for u, v := range vias {
		viaOf[normalizeUDID(u)] = parseActivateVia(v)
	}
	usb := map[string]bool{}
	for _, u := range udids {
		usb[udidKey(u)] = true
	}
	netSet := map[string]bool{}
	for u := range usbmuxNetworkUDIDs() {
		netSet[udidKey(u)] = true
	}
	for _, u := range NetworkUDIDs() {
		netSet[udidKey(u)] = true
	}
	if len(udids) == 0 && len(m.procs) > 0 {
		// 整表为空：可能是 ioreg 抖动，也可能是手机都拔到别的电脑。
		// 仍走连续 miss 拆除，禁止跳过对账把死隧道留到永远。
		slog.Warn("usb discovery empty with active tunnels, counting misses")
	}
	for _, udid := range usbTunnelsToDrop(usb, netSet, m.procs, m.misses, 2) {
		if p := m.procs[udid]; p != nil && p.cmd.Process != nil {
			_ = p.cmd.Process.Signal(os.Interrupt)
		}
		delete(m.procs, udid)
	}
	bin := iproxyBin()
	for udid, devPort := range ports {
		via, configured := viaOf[udid]
		wantNet := configured && via == activateViaNetwork
		if p, ok := m.procs[udid]; ok {
			if configured && p != nil && p.network != wantNet {
				if p.cmd != nil && p.cmd.Process != nil {
					_ = p.cmd.Process.Signal(os.Interrupt)
				}
				delete(m.procs, udid)
			} else {
				continue
			}
		}
		useNetwork := wantNet
		if !configured {
			useNetwork = !usb[udidKey(udid)] && netSet[udidKey(udid)]
		}
		if useNetwork {
			if !netSet[udidKey(udid)] {
				continue
			}
			local, err := freeLocalPort()
			if err != nil {
				continue
			}
			fwdBin := goIOSForwardBin()
			if fwdBin == "" {
				continue
			}
			args := []string{"forward", "--udid=" + udid, strconv.Itoa(local), strconv.Itoa(devPort)}
			cmd := exec.Command(fwdBin, args...)
			// 继承 os.Environ()：网关已注入 USBMUXD_SOCKET_ADDRESS=netmuxd。
			if err := cmd.Start(); err != nil {
				continue
			}
			p := &usbTunnelProc{cmd: cmd, port: local, network: true, done: make(chan struct{})}
			m.procs[udid] = p
			slog.Info("network tunnel up", "udid", udid[:8], "listen", fmt.Sprintf("127.0.0.1:%d", local))
			go func() {
				_ = cmd.Wait()
				close(p.done)
				m.mu.Lock()
				if cur, ok := m.procs[udid]; ok && cur == p {
					delete(m.procs, udid)
				}
				m.mu.Unlock()
			}()
			continue
		}
		if !usb[udidKey(udid)] {
			continue
		}
		if bin == "" {
			continue
		}
		local, err := freeLocalPort()
		if err != nil {
			continue
		}
		args := iproxyForwardArgs(udid, local, devPort, false)
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), bundleLibFallback()...)
		if err := cmd.Start(); err != nil {
			continue
		}
		p := &usbTunnelProc{cmd: cmd, port: local, done: make(chan struct{})}
		m.procs[udid] = p
		slog.Info("usb tunnel up", "udid", udid[:8], "listen", fmt.Sprintf("127.0.0.1:%d", local))
		go func() {
			_ = cmd.Wait()
			close(p.done)
			m.mu.Lock()
			if cur, ok := m.procs[udid]; ok && cur == p {
				delete(m.procs, udid)
			}
			m.mu.Unlock()
		}()
	}
}

// goIOSForwardBin 定位 go-ios 的 ios 可执行文件（Network 隧道用，读 USBMUXD_SOCKET_ADDRESS）。
func goIOSForwardBin() string {
	return lookTool("ios", "ios.exe")
}

// TunnelAddr 返回某 UDID 的本地隧道地址（"127.0.0.1:port"）；无可用隧道返回空串。
func TunnelAddr(udid string) string {
	addr, _, ok := tunnelInfo(udid)
	if !ok {
		return ""
	}
	return addr
}

// tunnelInfo 返回隧道地址与是否为 Network iproxy。
func tunnelInfo(udid string) (addr string, network bool, ok bool) {
	udid = normalizeUDID(udid)
	m := usbTunnels
	m.mu.Lock()
	defer m.mu.Unlock()
	p, exists := m.procs[udid]
	if !exists || p == nil {
		return "", false, false
	}
	select {
	case <-p.done:
		return "", false, false
	default:
		return fmt.Sprintf("127.0.0.1:%d", p.port), p.network, true
	}
}

func tunnelAddrForVia(udid, via string) string {
	addr, network, ok := tunnelInfo(udid)
	if !ok {
		return ""
	}
	if parseActivateVia(via) == activateViaNetwork {
		if network {
			return addr
		}
		return ""
	}
	if !network {
		return addr
	}
	return ""
}

func dropTunnel(udid string) {
	udid = normalizeUDID(udid)
	m := usbTunnels
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.procs[udid]; p != nil && p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	delete(m.procs, udid)
	delete(m.misses, udid)
}

// usbTunnelAlive 仅 USB iproxy 仍在（不含 iproxy -n）。Network 隧道不能当成插着 USB。
func usbTunnelAlive(udid string) bool {
	udid = normalizeUDID(udid)
	m := usbTunnels
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.procs[udid]
	if !ok || p == nil || p.network {
		return false
	}
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// addUsbmuxTunnelPorts 给 USB 与 Network 在线设备补默认 8100 隧道端口。
func addUsbmuxTunnelPorts(ports map[string]int) {
	if ports == nil {
		return
	}
	for _, u := range USBUDIDs() {
		if _, ok := ports[u]; !ok {
			ports[u] = 8100
		}
	}
	for _, u := range NetworkUDIDs() {
		if _, ok := ports[u]; !ok {
			ports[u] = 8100
		}
	}
}

// wdaProbeVia 只探指定通道：USB 只用 USB 隧道；Network 只用 Network 隧道或 Wi-Fi IP。
func wdaProbeVia(udid, ip string, port int, via string) WDAHealth {
	via = parseActivateVia(via)
	if port == 0 {
		port = 8100
	}
	if via == activateViaNetwork {
		if a := tunnelAddrForVia(udid, activateViaNetwork); a != "" {
			host, portStr, err := net.SplitHostPort(a)
			if err == nil {
				p, _ := strconv.Atoi(portStr)
				if p == 0 {
					p = 8100
				}
				h := CheckWDA(host, p, 3*time.Second)
				if h.OK {
					return h
				}
			}
		}
		if ip == "" {
			return WDAHealth{OK: false, Error: "Network 通道需要 Wi-Fi IP 或 Network 隧道"}
		}
		return CheckWDA(ip, port, 3*time.Second)
	}
	if a := tunnelAddrForVia(udid, activateViaUSB); a != "" {
		host, portStr, err := net.SplitHostPort(a)
		if err == nil {
			p, _ := strconv.Atoi(portStr)
			if p == 0 {
				p = 8100
			}
			return CheckWDA(host, p, 3*time.Second)
		}
	}
	return WDAHealth{OK: false, Error: "USB 通道无 USB 隧道"}
}

// wdaBaseURLFor 按通道选地址，不跨通道兜底。
func wdaBaseURLFor(udid, ip string, port int, via string) string {
	via = parseActivateVia(via)
	if port == 0 {
		port = 8100
	}
	if a := tunnelAddrForVia(udid, via); a != "" {
		return "http://" + a
	}
	if via == activateViaNetwork && ip != "" {
		return fmt.Sprintf("http://%s:%d", ip, port)
	}
	return ""
}

// resolveWDABaseURL 只返回当前通道上能答 /status 的地址。
func resolveWDABaseURL(udid, ip string, port int, via string) string {
	h := wdaProbeVia(udid, ip, port, via)
	if !h.OK {
		return ""
	}
	return wdaBaseURLFor(udid, ip, port, via)
}
