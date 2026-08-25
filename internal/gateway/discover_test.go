package gateway

import (
	"net"
	"strings"
	"testing"
	"time"
)

func TestIsPrivateIPv4(t *testing.T) {
	cases := map[string]bool{
		"10.0.0.1":       true,
		"172.16.0.1":     true,
		"172.31.255.255": true,
		"192.168.1.1":    true,
		"172.15.0.1":     false,
		"172.32.0.1":     false,
		"100.64.0.1":     false,
		"198.18.0.1":     false,
		"169.254.1.1":    false,
		"8.8.8.8":        false,
	}
	for s, want := range cases {
		if got := isPrivateIPv4(net.ParseIP(s).To4()); got != want {
			t.Errorf("isPrivateIPv4(%s) = %v, want %v", s, got, want)
		}
	}
}

func TestIOSMajor(t *testing.T) {
	cases := map[string]int{"15.8.8": 15, "16.4.1": 16, "18.4": 18, "": 0, "abc": 0}
	for in, want := range cases {
		if got := iosMajor(in); got != want {
			t.Errorf("iosMajor(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestValidSerial(t *testing.T) {
	cases := map[string]bool{
		"C38SG3S0HG00":             true,  // 经典 12 位
		"QP7X22L3JH":               true,  // 旧款 11 位
		"000081100001A1B2C3D4E5F6": true,  // 新式 24 位
		"short1":                   false, // 过短
		"":                         false,
		"ERROR: Unable to connect": false, // ideviceinfo 错误文本
		"含中文AAA111":                false, // 非字母数字
	}
	for s, want := range cases {
		if got := validSerial(s); got != want {
			t.Errorf("validSerial(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestParseIdeviceIDLines(t *testing.T) {
	usb, net := parseIdeviceIDLines(""+
		"5952499671171c733d6ef1345d4548a782686804 (Network)\n"+
		"5060c403afdee4c15a0edeab69dba0524e2ce592 (Network)\n", "usb")
	if len(usb) != 0 {
		t.Fatalf("Network-only listing usb=%v, want empty", usb)
	}
	if len(net) != 2 || net[0] != "5952499671171c733d6ef1345d4548a782686804" || net[1] != "5060c403afdee4c15a0edeab69dba0524e2ce592" {
		t.Fatalf("Network listing = %v", net)
	}

	usb, net = parseIdeviceIDLines(""+
		"00008120-000865D90A10C01E (USB)\n"+
		"4886579a97a96bad83b527862bab409b5a07c741 (Network)\n"+
		"00008120-000865D90A10C01E (Network)\n", "usb")
	if len(usb) != 1 || usb[0] != "00008120-000865D90A10C01E" {
		t.Fatalf("mixed usb=%v", usb)
	}
	if len(net) != 2 {
		t.Fatalf("mixed network=%v", net)
	}

	usb, net = parseIdeviceIDLines("4886579a97a96bad83b527862bab409b5a07c741\n", "usb")
	if len(usb) != 1 || usb[0] != "4886579a97a96bad83b527862bab409b5a07c741" || len(net) != 0 {
		t.Fatalf("bare -l usb=%v net=%v", usb, net)
	}

	usb, net = parseIdeviceIDLines("4886579a97a96bad83b527862bab409b5a07c741\n", "network")
	if len(usb) != 0 || len(net) != 1 || net[0] != "4886579a97a96bad83b527862bab409b5a07c741" {
		t.Fatalf("bare -n usb=%v net=%v", usb, net)
	}
}

func TestMergeDiscoveredIncludesNetworkOnly(t *testing.T) {
	got := mergeDiscovered(nil, nil, []string{"5060c403afdee4c15a0edeab69dba0524e2ce592"})
	if len(got) != 1 || got[0].UDID != "5060c403afdee4c15a0edeab69dba0524e2ce592" || got[0].Conn != "wifi" {
		t.Fatalf("network-only discover = %+v", got)
	}
}

func TestNetmuxdNetworkUDIDListFromSorted(t *testing.T) {
	if got := netmuxdNetworkUDIDListFrom(nil); len(got) != 0 {
		t.Fatalf("empty list = %v, want nil", got)
	}
	list := []string{
		"5060c403afdee4c15a0edeab69dba0524e2ce592",
		"4886579a97a96bad83b527862bab409b5a07c741",
		"5060c403afdee4c15a0edeab69dba0524e2ce592", // 重复按大小写无关去重
	}
	got := netmuxdNetworkUDIDListFrom(list)
	if len(got) != 2 ||
		got[0] != "4886579a97a96bad83b527862bab409b5a07c741" ||
		got[1] != "5060c403afdee4c15a0edeab69dba0524e2ce592" {
		t.Fatalf("sorted list = %v", got)
	}
	// iOS <16 必须保持小写原文，不能转大写。
	if got[1] != strings.ToLower(got[1]) {
		t.Fatalf("40-hex UDID must stay lowercase: %v", got)
	}
	merged := mergeDiscovered(nil, nil, got)
	if len(merged) != 2 || merged[0].Conn != "wifi" || merged[1].Conn != "wifi" {
		t.Fatalf("merge netmuxd network = %+v", merged)
	}
}

func TestMergeDiscoveredUSBWinsOverNetwork(t *testing.T) {
	u := "5060c403afdee4c15a0edeab69dba0524e2ce592"
	got := mergeDiscovered(
		[]DiscoveredDevice{{UDID: u, Name: "Phone"}},
		[]string{u},
		[]string{u},
	)
	if len(got) != 1 || got[0].Conn != "usb" || got[0].Name != "Phone" {
		t.Fatalf("usb+network merge = %+v", got)
	}
}

func TestMuxPresenceNetworkOnlyIsWifi(t *testing.T) {
	u := "5060c403afdee4c15a0edeab69dba0524e2ce592"
	present, usb, conn := muxPresence(u, nil, map[string]bool{u: true}, false, false)
	if !present || usb || conn != "wifi" {
		t.Fatalf("present=%v usb=%v conn=%q", present, usb, conn)
	}
	present, usb, conn = muxPresence(u, map[string]bool{u: true}, map[string]bool{u: true}, false, true)
	if !present || !usb || conn != "usb" {
		t.Fatalf("usb cable present=%v usb=%v conn=%q", present, usb, conn)
	}
	present, usb, conn = muxPresence(u, nil, nil, false, true)
	if !present || usb || conn != "wifi" {
		t.Fatalf("network tunnel present=%v usb=%v conn=%q", present, usb, conn)
	}
}

func TestDiscoverIncludesLiveNetworkUDIDs(t *testing.T) {
	bin := libiDeviceBin("idevice_id")
	if bin == "" {
		t.Skip("no idevice_id")
	}
	out, ok := runIdeviceID(bin, "-n")
	if !ok {
		t.Skip("idevice_id -n failed")
	}
	_, want := parseIdeviceIDLines(out, "network")
	if len(want) == 0 {
		t.Skip("no Network devices")
	}
	usbmuxTypedCache.mu.Lock()
	usbmuxTypedCache.at = time.Time{}
	usbmuxTypedCache.mu.Unlock()
	got := Discover()
	have := map[string]bool{}
	for _, d := range got {
		have[udidKey(d.UDID)] = true
	}
	for _, u := range want {
		if !have[udidKey(u)] {
			t.Fatalf("Discover missing Network %s: %+v", u, got)
		}
	}
	usbLive := map[string]bool{}
	if outL, okL := runIdeviceID(bin, "-l"); okL {
		listed, _ := parseIdeviceIDLines(outL, "usb")
		for _, u := range listed {
			usbLive[udidKey(u)] = true
		}
	}
	for _, n := range want {
		if usbLive[udidKey(n)] {
			continue
		}
		for _, u := range USBUDIDs() {
			if udidKey(u) == udidKey(n) {
				t.Fatalf("USBUDIDs must not include Network-only %s", u)
			}
		}
	}
}

func TestLooksLikeUDID(t *testing.T) {
	cases := map[string]bool{
		"4886579a97a96bad83b527862bab409b5a07c741": true,  // 老机型 40 位 hex（usbmux 原文）
		"00008120-000865d90a10c01e":                true,  // iPhone XS 及以后 8-16 带连字符（usbmux 原文）
		"00008120000865d90a10c01e":                 false, // 无连字符 24 位：iproxy/ideviceinfo 不认，非标准格式
		"a46bad8f-21e5-54c0-9960-e4aac7a4aa04":     false, // devicectl 的 CoreDevice UUID，不是 UDID
		"":                                         false,
		"001000001":                                false, // USB Hub serial
		"not-a-udid":                               false,
	}
	for s, want := range cases {
		if got := looksLikeUDID(s); got != want {
			t.Errorf("looksLikeUDID(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestLanFingerprintStableFormat(t *testing.T) {
	got := lanFingerprint()
	// 无网卡时允许空；有值时必须是排序后的 /24 列表。
	if got == "" {
		return
	}
	parts := strings.Split(got, ",")
	for i, p := range parts {
		if !strings.HasSuffix(p, "/24") {
			t.Fatalf("part %d = %q, want /24 suffix", i, p)
		}
		if i > 0 && parts[i-1] > p {
			t.Fatalf("not sorted: %q", got)
		}
	}
}

func TestWifiMatchByVendorUUID(t *testing.T) {
	devices := []Device{
		{UDID: "4886579a97a96bad83b527862bab409b5a07c741", VendorUUID: "AAAA-BBBB-CCCC"},
		{UDID: "no-uuid-device", VendorUUID: ""},
		{UDID: "other-device", VendorUUID: "DDDD-EEEE"},
	}
	scanned := []FoundWDA{
		{UUID: "AAAA-BBBB-CCCC", IP: "192.168.10.237", IOSIP: "192.168.10.237", IOSVersion: "15.8.8"},
		{UUID: "NOT-MATCHED", IP: "192.168.10.99"},
		{IP: "192.168.10.88"}, // 无 UUID 的扫描结果不参与匹配
	}
	got := wifiMatchByVendorUUID(devices, scanned)
	if len(got) != 1 {
		t.Fatalf("matched = %d, want 1: %#v", len(got), got)
	}
	f, ok := got["4886579A97A96BAD83B527862BAB409B5A07C741"]
	if !ok {
		t.Fatalf("expected first device matched, got %#v", got)
	}
	if f.IP != "192.168.10.237" {
		t.Fatalf("ip = %q, want 192.168.10.237", f.IP)
	}
}

func TestWifiMatchByVendorUUIDEmpty(t *testing.T) {
	got := wifiMatchByVendorUUID(nil, []FoundWDA{{UUID: "AAAA", IP: "10.0.0.2"}})
	if len(got) != 0 {
		t.Fatalf("matched = %#v, want none", got)
	}
	got = wifiMatchByVendorUUID([]Device{{UDID: "u", VendorUUID: "AAAA"}}, nil)
	if len(got) != 0 {
		t.Fatalf("matched = %#v, want none", got)
	}
}
