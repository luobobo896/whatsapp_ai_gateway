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

// USB 隧道：USB 直连时经 iproxy 转发到手机 8100（锁屏 Wi-Fi 休眠时更稳）。
// 拔线只拆除隧道进程，绝不 Stop 机上 WDA；之后由 wdaBaseURLFor / checkWDA 回退 Wi-Fi。
// 每个 USB 在线设备监听 127.0.0.1 随机本地端口。

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
		if set[udid] {
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
//  - USB 直连设备：iproxy 走 USB（默认）；
//  - 无线设备（usbmux ConnectionType=Network、无 USB）：iproxy -n 走 Network 隧道，
//    让网关能访问 WDA 的 loopback :8100（Wi-Fi 直连受 iOS 本地网络权限/绑定影响，隧道更稳）。
// ports 为需要隧道的 udid -> 设备 WDA 端口（来自 devices.json）。
func EnsureUSBTunnels(rawPorts map[string]int) {
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
	usb := map[string]bool{}
	for _, u := range udids {
		usb[normalizeUDID(u)] = true
	}
	netSet := map[string]bool{}
	for u := range usbmuxNetworkUDIDs() {
		netSet[normalizeUDID(u)] = true
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
	if bin == "" {
		return
	}
	for udid, devPort := range ports {
		if _, ok := m.procs[udid]; ok {
			continue
		}
		if usb[udid] {
			// USB 直连隧道
			local, err := freeLocalPort()
			if err != nil {
				continue
			}
			args := []string{"-u", udid}
			if runtime.GOOS == "windows" {
				// Windows 版 libimobiledevice iproxy 只认两个独立参数，
				// "LOCAL:DEVICE" 单参数会打印 usage 后立即退出（macOS 版兼容两种）。
				args = append(args, strconv.Itoa(local), strconv.Itoa(devPort))
			} else {
				args = append(args, fmt.Sprintf("%d:%d", local, devPort))
			}
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
			continue
		}
		// 无线设备：usbmux Network 隧道（iproxy -n，仅 macOS/Linux；Windows 走 go-ios/tidevice）
		if !netSet[udid] || runtime.GOOS == "windows" {
			continue
		}
		local, err := freeLocalPort()
		if err != nil {
			continue
		}
		args := []string{"-u", udid, "-n", fmt.Sprintf("%d:%d", local, devPort)}
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), bundleLibFallback()...)
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
	}
}

// TunnelAddr 返回某 UDID 的本地隧道地址（"127.0.0.1:port"）；无可用隧道返回空串。
func TunnelAddr(udid string) string {
	udid = normalizeUDID(udid)
	m := usbTunnels
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.procs[udid]; ok {
		select {
		case <-p.done:
			return ""
		default:
			return fmt.Sprintf("127.0.0.1:%d", p.port)
		}
	}
	return ""
}

// wdaBaseURLFor：USB 隧道优先；无隧道（含已拔线）回退 Wi-Fi IP:port。
func wdaBaseURLFor(udid, ip string, port int) string {
	if a := TunnelAddr(udid); a != "" {
		return "http://" + a
	}
	return fmt.Sprintf("http://%s:%d", ip, port)
}

// resolveWDABaseURL 选一条当前能答 /status 的地址：活着的 USB 隧道优先，否则 Wi-Fi。
// 拔线后 iproxy 可能短暂仍登记在表里但已不通；不能像 wdaBaseURLFor 那样死磕隧道，
// 否则群发会在看护回退 Wi-Fi 之前被拒。
func resolveWDABaseURL(udid, ip string, port int) string {
	if a := TunnelAddr(udid); a != "" {
		host, portStr, err := net.SplitHostPort(a)
		if err == nil {
			p, _ := strconv.Atoi(portStr)
			if p == 0 {
				p = 8100
			}
			if CheckWDA(host, p, 2*time.Second).OK {
				return "http://" + a
			}
		}
	}
	if ip == "" {
		return ""
	}
	if port == 0 {
		port = 8100
	}
	return fmt.Sprintf("http://%s:%d", ip, port)
}
