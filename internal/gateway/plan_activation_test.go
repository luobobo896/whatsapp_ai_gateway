package gateway

import (
	"runtime"
	"testing"
)

// TestPlanActivation 驱动本机「启用 Network」路径上已上船的纯函数：
// iTunes Buddy 优先、USB-only 不能当拔线安全、双通道时 Network 赢、Windows 禁止重启 AMDS。
func TestPlanActivation(t *testing.T) {
	itunes := "31274029-17647842852914356048"
	pair := "31273522-368570121651719656"
	if got := preferredWirelessBuddyID(itunes, pair); got != itunes {
		t.Fatalf("enable path must prefer iTunes WirelessBuddyID, got %q", got)
	}
	if !needsWirelessRebind(pair, preferredWirelessBuddyID(itunes, pair)) {
		t.Fatal("pair HostID still on the phone must re-bind from this Windows host")
	}

	udid := "4886579A97A96BAD83B527862BAB409B5A07C741"
	if unplugSafeFor(udid, "15.8.8", nil, map[string]bool{}) {
		t.Fatal("USB-only must not be unplug-safe")
	}
	if !unplugSafeFor(udid, "15.8.8", nil, map[string]bool{udid: true}) {
		t.Fatal("Network row must be unplug-safe on iOS 15")
	}

	got := parseUsbmuxConnectionTypes(`{"deviceList":[
		{"Udid":"4886579a97a96bad83b527862bab409b5a07c741","ConnectionType":"USB"},
		{"Udid":"4886579a97a96bad83b527862bab409b5a07c741","ConnectionType":"Network"}
	]}`)
	if got[udid] != "Network" {
		t.Fatalf("USB+Network fixture must select Network, got %q", got[udid])
	}

	if runtime.GOOS == "windows" {
		if err := restartUsbmuxd(); err != errWindowsUsbmuxRestartBlocked {
			t.Fatalf("Windows repair must refuse AMDS restart, got %v", err)
		}
	}
}
