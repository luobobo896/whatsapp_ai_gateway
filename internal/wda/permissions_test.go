package wda

import (
	"strings"
	"testing"
)

func TestIsPermissionAllowLabel(t *testing.T) {
	allow := []string{"允许", "Allow", "好", "OK", "允许访问本地网络", "WLAN 与蜂窝移动网"}
	for _, s := range allow {
		if !isPermissionAllowLabel(s) {
			t.Fatalf("want allow: %q", s)
		}
	}
	deny := []string{"不允许", "Don't Allow", "扫码注册", "注册", "手动输入注册码", ""}
	for _, s := range deny {
		if isPermissionAllowLabel(s) {
			t.Fatalf("must not tap: %q", s)
		}
	}
}

func TestPermissionSelectorsSkipRegistration(t *testing.T) {
	for _, sel := range permissionAllowSelectors {
		if containsAny(sel, "扫码", "注册", "qrcode", "enroll") {
			t.Fatalf("registration selector leaked: %s", sel)
		}
	}
}

func containsAny(s string, parts ...string) bool {
	for _, p := range parts {
		if p != "" && strings.Contains(s, p) {
			return true
		}
	}
	return false
}
