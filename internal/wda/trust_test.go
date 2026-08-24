package wda

import "testing"

func TestIsTrustActionLabel(t *testing.T) {
	allow := []string{"信任", "Trust", "验证", "安装", "Install", "信任“Example Co”"}
	for _, s := range allow {
		if !isTrustActionLabel(s) {
			t.Fatalf("want trust: %q", s)
		}
	}
	deny := []string{"不信任", "Don't Trust", "删除", "Delete", "取消", "Cancel", ""}
	for _, s := range deny {
		if isTrustActionLabel(s) {
			t.Fatalf("must not tap: %q", s)
		}
	}
}

func TestIsDeviceMgmtLabel(t *testing.T) {
	if !isDeviceMgmtLabel("VPN 与设备管理") || !isDeviceMgmtLabel("Device Management") {
		t.Fatal("mgmt labels")
	}
	if isDeviceMgmtLabel("") {
		t.Fatal("empty")
	}
}

func TestContainsLabelPredicateEscapesQuote(t *testing.T) {
	got := containsLabelPredicate("O'Brien")
	if want := `predicate string: label CONTAINS 'O\'Brien' OR name CONTAINS 'O\'Brien'`; got != want {
		t.Fatalf("got %q", got)
	}
}
