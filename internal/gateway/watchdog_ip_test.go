package gateway

import "testing"

func TestPreferredWifiIPPrefersWDASelfReport(t *testing.T) {
	// USB/扫描可能命中 EasyTier 代理网段 192.168.20.x，手机自报才是真实 Wi-Fi。
	got := preferredWifiIP("192.168.20.165", "192.168.10.237")
	if got != "192.168.10.237" {
		t.Fatalf("preferredWifiIP = %q, want phone self-report 192.168.10.237", got)
	}
}

func TestPreferredWifiIPFallbackScanHost(t *testing.T) {
	if got := preferredWifiIP("192.168.10.236", ""); got != "192.168.10.236" {
		t.Fatalf("empty report should keep scan host, got %q", got)
	}
	if got := preferredWifiIP("192.168.20.165", "169.254.1.1"); got != "192.168.20.165" {
		t.Fatalf("link-local report must be ignored, got %q", got)
	}
}

func TestSyncStoredWifiIP(t *testing.T) {
	d := &Device{UDID: "usb-phone", IP: "192.168.20.165"}
	if !syncStoredWifiIP(d, "192.168.10.237") {
		t.Fatal("should update stale overlay IP to WDA self-report")
	}
	if d.IP != "192.168.10.237" {
		t.Fatalf("IP = %q", d.IP)
	}
	if syncStoredWifiIP(d, "192.168.10.237") {
		t.Fatal("same IP should not report a change")
	}
	if syncStoredWifiIP(d, "not-an-ip") {
		t.Fatal("invalid report must not overwrite")
	}
}

func TestEvictWifiIPAndVendorUUID(t *testing.T) {
	devs := []Device{
		{UDID: "4886579a97a96bad83b527862bab409b5a07c741", IP: "192.168.10.237", VendorUUID: "CD02-USB"},
		{UDID: "00008120-000865d90a10c01e", IP: "192.168.10.237", VendorUUID: "CD02-USB"},
		{UDID: "other", IP: "192.168.10.236", VendorUUID: "other-uuid"},
	}
	n := evictWifiIP(devs, "4886579a97a96bad83b527862bab409b5a07c741", "192.168.10.237")
	if n != 1 || devs[1].IP != "" {
		t.Fatalf("should clear stolen IP from ghost device, n=%d ip=%q", n, devs[1].IP)
	}
	if devs[0].IP != "192.168.10.237" || devs[2].IP != "192.168.10.236" {
		t.Fatalf("must not touch owner or unrelated device: %+v", devs)
	}
	m := evictVendorUUID(devs, "4886579a97a96bad83b527862bab409b5a07c741", "CD02-USB")
	if m != 1 || devs[1].VendorUUID != "" {
		t.Fatalf("should clear stolen vendor uuid, m=%d uuid=%q", m, devs[1].VendorUUID)
	}
	if devs[0].VendorUUID != "CD02-USB" {
		t.Fatal("owner vendor uuid must stay")
	}
}

func TestFollowShouldSkipIPOwnedByOtherUSB(t *testing.T) {
	devs := []Device{
		{UDID: "usb-owner", IP: "192.168.10.237"},
		{UDID: "stale", IP: "192.168.20.165"},
	}
	usb := map[string]bool{"usb-owner": true}
	if !ipOwnedByOtherUSB(devs, usb, "stale", "192.168.10.237") {
		t.Fatal("follow must not steal USB owner's current Wi-Fi IP")
	}
	if ipOwnedByOtherUSB(devs, usb, "usb-owner", "192.168.10.237") {
		t.Fatal("owner itself is allowed")
	}
}
