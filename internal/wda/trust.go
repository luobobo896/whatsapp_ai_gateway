package wda

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const settingsBundleID = "com.apple.Preferences"

// 个人「开发者 App」和企业「企业级 App」都在「VPN 与设备管理」里点信任。
var deviceMgmtURLs = []string{
	"App-prefs:root=General&path=ManagedConfigurationList",
	"prefs:root=General&path=ManagedConfigurationList",
	"App-prefs:root=General&path=VPN",
}

var trustButtonSelectors = []string{
	`predicate string: type == 'XCUIElementTypeButton' AND (label == '信任' OR name == '信任' OR label == 'Trust' OR name == 'Trust')`,
	`predicate string: type == 'XCUIElementTypeButton' AND (label CONTAINS '信任' OR name CONTAINS '信任' OR label CONTAINS 'Trust' OR name CONTAINS 'Trust')`,
	`predicate string: type == 'XCUIElementTypeButton' AND (label == '验证' OR name == '验证' OR label CONTAINS 'Verify')`,
	`predicate string: type == 'XCUIElementTypeButton' AND (label == '安装' OR name == '安装' OR label == 'Install' OR name == 'Install')`,
}

func isTrustActionLabel(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	deny := []string{"不信任", "Don't Trust", "Don’t Trust", "删除", "Delete", "移除", "Remove", "取消", "Cancel"}
	for _, d := range deny {
		if strings.Contains(t, d) {
			return false
		}
	}
	return t == "信任" || t == "Trust" || t == "验证" || t == "安装" || t == "Install" ||
		strings.Contains(t, "信任") || strings.Contains(t, "Trust") || strings.Contains(t, "Verify")
}

func isDeviceMgmtLabel(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	keys := []string{
		"VPN", "设备管理", "Device Management", "描述文件", "Profile",
		"Managed", "开发者", "Developer", "企业级", "Enterprise",
	}
	for _, k := range keys {
		if strings.Contains(t, k) {
			return true
		}
	}
	return false
}

func containsLabelPredicate(text string) string {
	s := strings.ReplaceAll(text, `'`, `\'`)
	return fmt.Sprintf(`predicate string: label CONTAINS '%s' OR name CONTAINS '%s'`, s, s)
}

// TrustDeveloper 打开系统「VPN 与设备管理」，点信任开发者/企业证书。
// 个人和企业是同一套设置页。找不到按钮返回 nil（激活本身已成功）。
func TrustDeveloper(ctx context.Context, client *Client, teamName string) error {
	if client == nil {
		return nil
	}
	sid, err := client.CreateBareSession(ctx)
	if err != nil || sid == "" {
		return err
	}
	defer func() { _ = client.DeleteSession(context.WithoutCancel(ctx), sid) }()

	_ = client.AcceptAlert(ctx, sid)
	if tapTrustButtons(ctx, client, sid) {
		_ = client.AcceptAlert(ctx, sid)
		return nil
	}
	for _, u := range deviceMgmtURLs {
		_ = client.OpenDeepLink(ctx, sid, u, settingsBundleID)
		time.Sleep(500 * time.Millisecond)
		_ = client.AcceptAlert(ctx, sid)
		if teamName != "" {
			_ = tapFirst(ctx, client, sid, containsLabelPredicate(teamName))
			time.Sleep(300 * time.Millisecond)
		}
		for _, sel := range []string{
			containsLabelPredicate("设备管理"),
			containsLabelPredicate("Device Management"),
			containsLabelPredicate("开发者"),
			containsLabelPredicate("Developer App"),
			containsLabelPredicate("企业级"),
			containsLabelPredicate("Enterprise App"),
		} {
			if tapFirst(ctx, client, sid, sel) {
				time.Sleep(300 * time.Millisecond)
				break
			}
		}
		if tapTrustButtons(ctx, client, sid) {
			_ = client.AcceptAlert(ctx, sid)
			_ = tapTrustButtons(ctx, client, sid)
			return nil
		}
	}
	return nil
}

func tapTrustButtons(ctx context.Context, client *Client, sid string) bool {
	tapped := false
	for _, sel := range trustButtonSelectors {
		id, err := findElementBySelector(ctx, client, sid, sel)
		if err != nil || id == "" {
			continue
		}
		if label, lerr := client.ElementText(ctx, sid, id); lerr == nil && label != "" && !isTrustActionLabel(label) {
			continue
		}
		if client.Click(ctx, sid, id) == nil {
			tapped = true
			time.Sleep(300 * time.Millisecond)
			_ = client.AcceptAlert(ctx, sid)
		}
	}
	return tapped
}

func tapFirst(ctx context.Context, client *Client, sid, sel string) bool {
	id, err := findElementBySelector(ctx, client, sid, sel)
	if err != nil || id == "" {
		return false
	}
	return client.Click(ctx, sid, id) == nil
}
