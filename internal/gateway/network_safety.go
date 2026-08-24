package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

// usbmuxNetworkUDIDs 返回 usbmux 里带有 Network（无线调试）条目的设备 UDID 集合（大写）。
// iOS ≤16 拔线保活的先决条件就是 usbmux 有 Network 条目：wifi-runwda 会优先选它，
// 让 XCTest 会话骑在 WiFi 上，拔 USB 才不会拆。返回空集合表示「无该条目 → 拔线必断」。
func usbmuxNetworkUDIDs() map[string]bool {
	out := map[string]bool{}
	bin := lookTool("ios", "ios.exe")
	if bin == "" {
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "list", "--details")
	cmd.Env = append(os.Environ(), bundleLibFallback()...)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return out
	}
	types := parseUsbmuxConnectionTypes(string(raw))
	if types == nil {
		text := string(raw)
		if len(text) > 300 {
			text = text[:300] + "…"
		}
		slog.Warn("parse ios list --details failed", "raw", text)
		return out
	}
	for udid, ct := range types {
		if strings.EqualFold(ct, "Network") {
			out[strings.ToUpper(udid)] = true
		}
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
