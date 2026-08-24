package wda

import (
	"context"
	"strings"
	"time"
)

// 当前 WhatsApp Device Agent 已去掉扫码注册页，激活后只需点系统/应用的权限确认。
// 只点「允许」类按钮，不点注册、扫码、不允许。
var permissionAllowSelectors = []string{
	`predicate string: type == 'XCUIElementTypeButton' AND (label == '允许' OR name == '允许' OR label == 'Allow' OR name == 'Allow')`,
	`predicate string: type == 'XCUIElementTypeButton' AND (label == '好' OR name == '好' OR label == 'OK' OR name == 'OK')`,
	`predicate string: type == 'XCUIElementTypeButton' AND (label CONTAINS '本地网络' OR name CONTAINS '本地网络' OR label CONTAINS 'Local Network' OR name CONTAINS 'Local Network')`,
	`predicate string: type == 'XCUIElementTypeButton' AND (label CONTAINS '无线局域网' OR name CONTAINS '无线局域网' OR label CONTAINS 'WLAN')`,
}

func isPermissionAllowLabel(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	if strings.Contains(t, "不允许") || strings.Contains(t, "Don't Allow") || strings.Contains(t, "Don’t Allow") {
		return false
	}
	if strings.Contains(t, "扫码") || strings.Contains(t, "注册") {
		return false
	}
	switch t {
	case "允许", "Allow", "好", "OK", "Ok":
		return true
	}
	return strings.Contains(t, "本地网络") || strings.Contains(t, "Local Network") ||
		strings.Contains(t, "无线局域网") || strings.Contains(t, "WLAN")
}

// TapPermissionAllows 接受系统弹窗，并点 Agent 上的网络等权限按钮。找不到按钮不算失败。
func TapPermissionAllows(ctx context.Context, client *Client) {
	if client == nil {
		return
	}
	sid, err := client.CreateBareSession(ctx)
	if err != nil || sid == "" {
		return
	}
	defer func() { _ = client.DeleteSession(context.WithoutCancel(ctx), sid) }()

	_ = client.AcceptAlert(ctx, sid)
	for i := 0; i < 3; i++ {
		tapped := false
		for _, sel := range permissionAllowSelectors {
			id, ferr := findElementBySelector(ctx, client, sid, sel)
			if ferr != nil || id == "" {
				continue
			}
			if label, lerr := client.ElementText(ctx, sid, id); lerr == nil && label != "" && !isPermissionAllowLabel(label) {
				continue
			}
			if client.Click(ctx, sid, id) == nil {
				tapped = true
				time.Sleep(300 * time.Millisecond)
				_ = client.AcceptAlert(ctx, sid)
				break
			}
		}
		if !tapped {
			return
		}
	}
}
