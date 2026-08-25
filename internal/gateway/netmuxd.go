//go:build windows

package gateway

// netmuxd.go：Windows 专用。AMDS（Apple Mobile Device Service）只枚举 USB、
// 从不产生 ConnectionType=Network 条目；网关在本机拉起 netmuxd（shim 模式）：
//   - USB 请求原样转发上游 AMDS（127.0.0.1:27015），不抢 Apple 驱动、不重启服务；
//   - Network 设备由 mDNS 发现（_apple-mobdev2._tcp.local），配对记录取自
//     C:/ProgramData/Apple/Lockdown，并用 com.apple.mobile.heartbeat 会话保活，
//     拔掉 USB 后无线 WDA 不再因 iOS 网络 lockdown 会话过期而断开。
// 随后把 USBMUXD_SOCKET_ADDRESS 注入环境变量，使 go-ios 子进程（ios / wifi-runwda）
// 在无线激活时看到并走 Network 通道，与 Mac 上 usbmuxd 的行为一致。

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"

	"golang.org/x/sys/windows"
)

const (
	netmuxdListenAddr  = "127.0.0.1:27016"
	netmuxdPort        = 27016
	netmuxdUpstream    = "127.0.0.1:27015"
	netmuxdEnvName     = "USBMUXD_SOCKET_ADDRESS"
	netmuxdProcessName = "netmuxd.exe"
)

// netmuxdBin 定位 netmuxd.exe：PATH → WDA_GATEWAY_RESOURCES/bin → 仓库 tools/。
func netmuxdBin() string {
	if p, err := exec.LookPath(netmuxdProcessName); err == nil {
		return p
	}
	var candidates []string
	if res := os.Getenv("WDA_GATEWAY_RESOURCES"); res != "" {
		candidates = append(candidates, filepath.Join(res, "bin", netmuxdProcessName))
	}
	candidates = append(candidates, repoToolPaths([]string{netmuxdProcessName})...)
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// netmuxdArgs 构造 netmuxd 启动参数：shim 模式监听本机，USB 经上游 AMDS，
// Network 由 netmuxd 自身 mDNS 发现 + heartbeat 保活。
func netmuxdArgs() []string {
	return []string{
		"--port", strconv.Itoa(netmuxdPort),
		"--upstream-usbmuxd", netmuxdUpstream,
	}
}

// syncNetmuxd 保证 Windows 上 netmuxd 在运行且环境变量已注入；进程退出后下一轮看护拉起。
// 与 Mac 的 usbmuxd 一致：不依赖已配置设备，任何配对过且在同一 Wi-Fi 的手机都能被发现。
// 调用方：watchOnce 每轮。
func (g *Gateway) syncNetmuxd() {
	if runtime.GOOS != "windows" {
		return
	}
	g.netmuxdMu.Lock()
	defer g.netmuxdMu.Unlock()
	if g.netmuxdCmd != nil && g.netmuxdCmd.Process != nil {
		select {
		case <-g.netmuxdDone:
			slog.Warn("netmuxd exited, restarting", "devices", len(g.Cfg.Devices))
		default:
			return
		}
	}
	bin := netmuxdBin()
	if bin == "" {
		slog.Warn("netmuxd 未找到，Windows 无线 Network 激活不可用（可先 USB 激活）")
		return
	}
	cmd := exec.Command(bin, netmuxdArgs()...)
	cmd.SysProcAttr = &windows.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP,
	}
	if err := cmd.Start(); err != nil {
		slog.Error("start netmuxd failed", "error", err)
		return
	}
	done := make(chan struct{})
	g.netmuxdCmd = cmd
	g.netmuxdDone = done
	_ = os.Setenv(netmuxdEnvName, "tcp://"+netmuxdListenAddr)
	slog.Info("netmuxd started", "listen", netmuxdListenAddr, "upstream", netmuxdUpstream)
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
}

// StopNetmuxd 网关退出时清理 netmuxd 进程与环境变量。
func (g *Gateway) StopNetmuxd() {
	g.netmuxdMu.Lock()
	defer g.netmuxdMu.Unlock()
	g.stopNetmuxdLocked()
}

func (g *Gateway) stopNetmuxdLocked() {
	if g.netmuxdCmd != nil && g.netmuxdCmd.Process != nil {
		_ = g.netmuxdCmd.Process.Kill()
		_, _ = g.netmuxdCmd.Process.Wait()
		g.netmuxdCmd = nil
		g.netmuxdDone = nil
	}
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/IM", netmuxdProcessName, "/F").Run()
	}
	_ = os.Unsetenv(netmuxdEnvName)
}
