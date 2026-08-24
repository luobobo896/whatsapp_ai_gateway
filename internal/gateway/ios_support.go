package gateway

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"
)

// iOS 15–16 走 usbmux + Developer Disk Image；iOS 17+ 必须先有 go-ios RemoteXPC 隧道。
// 开发者模式从 iOS 16 起强制。这些阈值来自 Apple CoreDevice / go-ios 行为，不是猜测。

const (
	iosDDIMaxMajor      = 16
	iosTunnelMinMajor   = 17
	iosDevModeMinMajor  = 16
	iosUserspaceMinMinr = 4 // 用户态隧道：iOS 17.4+（17.0–17.3 需内核 TUN）
)

type iosSupportPlan struct {
	Version     string
	Major       int
	Minor       int
	NeedDDI     bool
	NeedTunnel  bool
	NeedDevMode bool
	UserspaceOK bool
}

var (
	iosVerMu    sync.Mutex
	iosVerCache = map[string]string{}
)

func rememberIOSVersion(udid, version string) {
	udid = strings.TrimSpace(udid)
	version = strings.TrimSpace(version)
	if udid == "" || version == "" {
		return
	}
	iosVerMu.Lock()
	iosVerCache[udid] = version
	iosVerMu.Unlock()
}

func cachedIOSVersion(udid string) string {
	iosVerMu.Lock()
	defer iosVerMu.Unlock()
	return iosVerCache[udid]
}

func iosMinor(v string) int {
	parts := strings.Split(strings.TrimSpace(v), ".")
	if len(parts) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(parts[1])
	return n
}

func planForIOS(version string) iosSupportPlan {
	p := iosSupportPlan{Version: strings.TrimSpace(version)}
	p.Major = iosMajor(p.Version)
	p.Minor = iosMinor(p.Version)
	if p.Major > 0 && p.Major <= iosDDIMaxMajor {
		p.NeedDDI = true
	}
	if p.Major >= iosTunnelMinMajor {
		p.NeedTunnel = true
	}
	if p.Major >= iosDevModeMinMajor {
		p.NeedDevMode = true
	}
	// 未知版本不当成 17+，避免老机被错误挡住；连字符 UDID 另由 likelyNeedsTunnel 处理。
	p.UserspaceOK = !p.NeedTunnel || p.Major > iosTunnelMinMajor || p.Minor >= iosUserspaceMinMinr
	return p
}

func needsRemoteXPCTunnel(version string) bool {
	return planForIOS(version).NeedTunnel
}

func needsDeveloperDiskImage(version string) bool {
	return planForIOS(version).NeedDDI
}

func needsDeveloperMode(version string) bool {
	return planForIOS(version).NeedDevMode
}

func userspaceTunnelSupported(version string) bool {
	return planForIOS(version).UserspaceOK
}

// likelyNeedsTunnel：版本已确认 ≥17，或版本未知但 UDID 是新格式（iPhone XS+，可能是 16 或 17+）。
// 未知+新 UDID 只用来决定是否拉起隧道守护进程，不阻塞等该机隧道出现。
func likelyNeedsTunnel(udid, version string) bool {
	if needsRemoteXPCTunnel(version) {
		return true
	}
	return strings.TrimSpace(version) == "" && strings.Contains(udid, "-")
}

// resolveIOSVersion 激活走最短路径：已有版本直接用，避免每次打满 ideviceinfo。
func resolveIOSVersion(udid, persisted string) string {
	if v := strings.TrimSpace(persisted); v != "" {
		rememberIOSVersion(udid, v)
		return v
	}
	if v := cachedIOSVersion(udid); v != "" {
		return v
	}
	if v := strings.TrimSpace(ideviceInfoValue(udid, "ProductVersion")); v != "" {
		rememberIOSVersion(udid, v)
		return v
	}
	if v := goiosProductVersion(udid); v != "" {
		rememberIOSVersion(udid, v)
		return v
	}
	return ""
}

func goiosProductVersion(udid string) string {
	bin := lookTool("ios", "ios.exe")
	if bin == "" || udid == "" {
		return ""
	}
	out, err := runTool(bin, []string{"list", "--details"}, 12*time.Second)
	if err != nil {
		return ""
	}
	return parseGoIOSDeviceListVersion(out, udid)
}

func parseDevModeEnabled(out string) (enabled bool, ok bool) {
	s := strings.TrimSpace(out)
	if s == "" {
		return false, false
	}
	// 丢掉 go-ios 打到 stdout 的日志行，只留 JSON / 纯值。
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "{") && strings.Contains(line, "\"time\"") && strings.Contains(line, "\"msg\"") {
			continue
		}
		if line == "true" || line == "false" {
			return line == "true", true
		}
		if strings.HasPrefix(line, "{") {
			var obj map[string]any
			if json.Unmarshal([]byte(line), &obj) != nil {
				continue
			}
			for _, key := range []string{"enabled", "Enabled", "developerModeEnabled", "DeveloperModeStatus"} {
				if v, exists := obj[key]; exists {
					switch t := v.(type) {
					case bool:
						return t, true
					case float64:
						return t != 0, true
					case string:
						low := strings.ToLower(strings.TrimSpace(t))
						if low == "true" || low == "1" || low == "enabled" {
							return true, true
						}
						if low == "false" || low == "0" || low == "disabled" {
							return false, true
						}
					}
				}
			}
		}
	}
	low := strings.ToLower(s)
	if strings.Contains(low, `"enabled": true`) || strings.Contains(low, `"enabled":true`) {
		return true, true
	}
	if strings.Contains(low, `"enabled": false`) || strings.Contains(low, `"enabled":false`) {
		return false, true
	}
	return false, false
}

func parseGoIOSDeviceListVersion(out, udid string) string {
	var wrap struct {
		DeviceList []struct {
			Udid           string `json:"Udid"`
			ProductVersion string `json:"ProductVersion"`
		} `json:"deviceList"`
	}
	if json.Unmarshal([]byte(extractJSONObject(out)), &wrap) != nil {
		return ""
	}
	for _, d := range wrap.DeviceList {
		if strings.EqualFold(d.Udid, udid) {
			return strings.TrimSpace(d.ProductVersion)
		}
	}
	return ""
}

func extractJSONObject(s string) string {
	s = strings.TrimSpace(s)
	i := strings.Index(s, "{")
	j := strings.LastIndex(s, "}")
	if i >= 0 && j > i {
		return s[i : j+1]
	}
	return s
}

func extractJSONArray(s string) string {
	s = strings.TrimSpace(s)
	i := strings.Index(s, "[")
	j := strings.LastIndex(s, "]")
	if i >= 0 && j > i {
		return s[i : j+1]
	}
	return ""
}
