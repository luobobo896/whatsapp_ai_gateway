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
)

// USB 隧道：USB 直连设备的 WDA 流量经 libimobiledevice 的 iproxy 转发到手机 8100，
// 不再依赖手机 Wi-Fi（iOS 锁屏 Wi-Fi 休眠/DHCP 变更会造成"USB 在、WDA 活、
// 网络不通"的假在线）。每个 USB 在线设备监听 127.0.0.1 随机本地端口。

// usbTunnelProc 一台设备的 iproxy 进程。
type usbTunnelProc struct {
	cmd  *exec.Cmd
	port int
	done chan struct{}
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

// EnsureUSBTunnels 按当前 USB 设备与目标端口对账隧道（看护循环每轮调用）。
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
	if len(udids) == 0 && len(m.procs) > 0 {
		// ioreg 偶发失败/空结果：视为发现层抖动，本轮不对账，
		// 避免把全部隧道拆掉造成所有设备同时掉线（真拔线由连续确认机制兜底）。
		slog.Warn("usb discovery empty with active tunnels, skip reconcile")
		return
	}
	usb := map[string]bool{}
	for _, u := range udids {
		usb[normalizeUDID(u)] = true
	}
	// 拆除防抖：单台设备连续 2 轮未在 USB 列表确认才拆，单次抖动不误杀；
	// 只拆确认消失的设备，其余隧道不受影响（停一台只停一台）。
	for udid, p := range m.procs {
		if usb[udid] {
			delete(m.misses, udid)
			continue
		}
		m.misses[udid]++
		if m.misses[udid] < 2 {
			continue
		}
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Signal(os.Interrupt)
		}
		delete(m.procs, udid)
		delete(m.misses, udid)
	}
	bin := iproxyBin()
	if bin == "" {
		return
	}
	for udid, devPort := range ports {
		if _, ok := m.procs[udid]; ok {
			continue
		}
		if !usb[udid] {
			continue
		}
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
	}
}

// TunnelAddr 返回某 UDID 的本地隧道地址（"127.0.0.1:port"）；无可用隧道返回空串。
func TunnelAddr(udid string) string {
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

// wdaBaseURLFor 解析某设备的 WDA 访问地址：USB 隧道优先（不依赖手机 Wi-Fi），
// 无隧道回退 Wi-Fi IP:port。
func wdaBaseURLFor(udid, ip string, port int) string {
	if a := TunnelAddr(udid); a != "" {
		return "http://" + a
	}
	return fmt.Sprintf("http://%s:%d", ip, port)
}
