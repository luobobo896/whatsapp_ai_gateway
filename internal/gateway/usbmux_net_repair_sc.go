package gateway

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// errWindowsUsbmuxRestartBlocked 点「立即修复」时返回给页面，避免再拆 USB。
var errWindowsUsbmuxRestartBlocked = errors.New("Windows 重启 Apple 设备服务不会出现 ConnectionType=Network，反而会让 USB 设备从列表消失。请保持 USB 插入；探活用手机 Wi-Fi :8100/status。无线配对请在已能 idevice_id -n 的 Mac 上做")

// appleUsbmuxServiceCandidates Windows 上可能提供 usbmux 的服务名（按常见程度）。
func appleUsbmuxServiceCandidates() []string {
	return []string{
		"Apple Mobile Device Service",
		"Apple Mobile Device",
		"Apple Devices Service",
		"Apple Devices",
	}
}

var scStateRe = regexp.MustCompile(`(?i)STATE\s*:\s*(\d+)`)

// parseScQuery 解析 `sc query` 输出。
// exists=false：服务未安装（1060）。state 为 Windows 服务状态码：1 已停、2 正在启动、3 正在停止、4 运行中。
func parseScQuery(out string) (state int, exists bool) {
	s := strings.TrimSpace(out)
	if s == "" {
		return 0, false
	}
	if strings.Contains(s, "1060") || strings.Contains(s, "does not exist") || strings.Contains(s, "未安装") {
		return 0, false
	}
	m := scStateRe.FindStringSubmatch(s)
	if len(m) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// scStartOK：start 成功，或 1056（服务已在跑）视为成功。
func scStartOK(err error) bool {
	if err == nil {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "1056")
}

func firstExistingService(names []string, exists map[string]bool) string {
	for _, n := range names {
		if exists[n] {
			return n
		}
	}
	return ""
}
