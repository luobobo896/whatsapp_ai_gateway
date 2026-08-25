package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

var netmuxdNetCache struct {
	mu   sync.Mutex
	at   time.Time
	set  map[string]bool
	list []string // Network UDID 原文（iOS <16 为小写 40-hex；iOS 16+ 为带连字符大写）
}

// usbmuxNetworkUDIDs 返回 usbmux 里带有 Network（无线调试）条目的设备 UDID 集合（大写）。
// iOS ≤16 拔线保活的先决条件就是 usbmux 有 Network 条目：wifi-runwda 会优先选它，
// 让 XCTest 会话骑在 WiFi 上，拔 USB 才不会拆。返回空集合表示「无该条目 → 拔线必断」。
// 结果缓存 1s，避免看护每轮/每台设备重复拉起 `ios list --details` 子进程。
func usbmuxNetworkUDIDs() map[string]bool {
	netmuxdNetCache.mu.Lock()
	defer netmuxdNetCache.mu.Unlock()
	if !netmuxdNetCache.at.IsZero() && time.Since(netmuxdNetCache.at) < time.Second {
		return netmuxdNetCache.set
	}
	set, list := usbmuxNetworkUDIDsUncached()
	netmuxdNetCache.set, netmuxdNetCache.list, netmuxdNetCache.at = set, list, time.Now()
	return set
}

// usbmuxNetworkUDIDList 返回 Network 设备的 UDID 原文列表，供 Discover 合并。
// 保持 netmuxd 上报的原始大小写：iOS <16 是 40 位小写 hex，转大写会与配对记录、
// 激活工具（wifi-lockdown / ios runwda）对不上。与 usbmuxNetworkUDIDs 共享缓存。
func usbmuxNetworkUDIDList() []string {
	netmuxdNetCache.mu.Lock()
	defer netmuxdNetCache.mu.Unlock()
	if !netmuxdNetCache.at.IsZero() && time.Since(netmuxdNetCache.at) < time.Second {
		return append([]string(nil), netmuxdNetCache.list...)
	}
	set, list := usbmuxNetworkUDIDsUncached()
	netmuxdNetCache.set, netmuxdNetCache.list, netmuxdNetCache.at = set, list, time.Now()
	return append([]string(nil), list...)
}

func usbmuxNetworkUDIDsUncached() (map[string]bool, []string) {
	out := map[string]bool{}
	bin := lookTool("ios", "ios.exe")
	if bin == "" {
		return out, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "list", "--details")
	cmd.Env = append(os.Environ(), bundleLibFallback()...)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return out, nil
	}
	list := parseUsbmuxNetworkUDIDs(string(raw))
	if list == nil {
		text := string(raw)
		if len(text) > 300 {
			text = text[:300] + "…"
		}
		slog.Warn("parse ios list --details failed", "raw", text)
		return out, nil
	}
	for _, u := range list {
		out[strings.ToUpper(u)] = true
	}
	return out, list
}

// parseUsbmuxNetworkUDIDs 解析 `ios list --details`，返回 ConnectionType=Network 的
// UDID 原文（按上报大小写原样保留，去重）。解析失败返回 nil。
func parseUsbmuxNetworkUDIDs(raw string) []string {
	arr := extractJSONArray(raw)
	if arr == "" {
		return nil
	}
	var list []struct {
		Udid           string `json:"Udid"`
		ConnectionType string `json:"ConnectionType"`
	}
	if json.Unmarshal([]byte(arr), &list) != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, d := range list {
		if d.Udid == "" || !strings.EqualFold(d.ConnectionType, "Network") {
			continue
		}
		key := strings.ToUpper(d.Udid)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, d.Udid)
	}
	return out
}

// parseUsbmuxConnectionTypes 解析 `ios list --details` 输出，返回 UDID(大写)->ConnectionType。
// go-ios 会把 JSON 日志行（{...}）和结果行混在 stdout，用 extractJSONArray 取 deviceList。
func parseUsbmuxConnectionTypes(raw string) map[string]string {
	out := map[string]string{}
	arr := extractJSONArray(raw)
	if arr == "" {
		return nil
	}
	var list []struct {
		Udid           string `json:"Udid"`
		ConnectionType string `json:"ConnectionType"`
	}
	if json.Unmarshal([]byte(arr), &list) != nil {
		return nil
	}
	for _, d := range list {
		if d.Udid == "" {
			continue
		}
		u := strings.ToUpper(d.Udid)
		// 同一 UDID 可能同时有 Network + USB 两行：Network 一定优先，避免被 USB 覆盖。
		if d.ConnectionType == "Network" || out[u] == "" {
			out[u] = d.ConnectionType
		}
	}
	return out
}

// unplugSafeFor 判断该设备「拔掉 USB 后 WDA 是否仍可保持/被访问」。
//   - iOS 17+：依赖 go-ios 隧道在（tunnelSet）；隧道若骑 WiFi 则可续命，仍建议真机验证。
//   - iOS ≤16：依赖 usbmux Network 条目（netSet）——wifi-runwda 走无线拉起才拔线不断。
func unplugSafeFor(udid, iosVersion string, tunnelSet, netSet map[string]bool) bool {
	key := strings.ToUpper(strings.TrimSpace(udid))
	if key == "" {
		return false
	}
	if needsRemoteXPCTunnel(iosVersion) {
		return tunnelSet[key]
	}
	return netSet[key]
}
