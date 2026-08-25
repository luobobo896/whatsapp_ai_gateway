package gateway

import (
	"strings"
	"testing"
)

func TestParseUsbmuxConnectionTypes(t *testing.T) {
	raw := `{"time":"2026-08-24T10:00:00.000+08:00","level":"WARN","msg":"go-ios agent is not running."}
{"deviceList":[{"Udid":"5952499671171c733d6ef1345d4548a782686804","ProductVersion":"15.8.8","ConnectionType":"USB"},{"Udid":"00008120-000865D90A10C01E","ProductVersion":"18.5","ConnectionType":"Network"},{"Udid":"4886579a97a96bad83b527862bab409b5a07c741","ConnectionType":"Network"}]}`
	got := parseUsbmuxConnectionTypes(raw)
	if got == nil {
		t.Fatal("parse returned nil")
	}
	if got["5952499671171C733D6EF1345D4548A782686804"] != "USB" {
		t.Fatalf("USB udid type=%q want USB", got["5952499671171C733D6EF1345D4548A782686804"])
	}
	if got["00008120-000865D90A10C01E"] != "Network" {
		t.Fatalf("hyphen udid type=%q want Network", got["00008120-000865D90A10C01E"])
	}
	if len(got) != 3 {
		t.Fatalf("got %d devices, want 3", len(got))
	}
}

func TestUsbmuxNetworkUDIDsFiltersNetwork(t *testing.T) {
	raw := `{"deviceList":[{"Udid":"old40hex...","ConnectionType":"USB"},{"Udid":"00008120-000865D90A10C01E","ConnectionType":"Network"}]}`
	types := parseUsbmuxConnectionTypes(raw)
	set := map[string]bool{}
	for udid, ct := range types {
		if strings.EqualFold(ct, "Network") {
			set[strings.ToUpper(udid)] = true
		}
	}
	if set["00008120-000865D90A10C01E"] != true {
		t.Fatal("Network device missing from set")
	}
	if len(set) != 1 {
		t.Fatalf("set size=%d want 1 (USB device must be excluded)", len(set))
	}
}

func TestParseUsbmuxConnectionTypesMalformed(t *testing.T) {
	if got := parseUsbmuxConnectionTypes("not json at all"); got != nil {
		t.Fatalf("malformed should return nil, got %v", got)
	}
}

func TestParseUsbmuxNetworkUDIDsKeepsOriginalCase(t *testing.T) {
	raw := `{"deviceList":[{"Udid":"5060c403afdee4c15a0edeab69dba0524e2ce592","ConnectionType":"USB"},{"Udid":"5060c403afdee4c15a0edeab69dba0524e2ce592","ConnectionType":"Network"},{"Udid":"4886579a97a96bad83b527862bab409b5a07c741","ConnectionType":"Network"},{"Udid":"00008120-000865D90A10C01E","ConnectionType":"Network"}]}`
	got := parseUsbmuxNetworkUDIDs(raw)
	if got == nil {
		t.Fatal("parse returned nil")
	}
	// iOS <16 的 40-hex UDID 必须保持小写原文。
	if len(got) != 3 ||
		got[0] != "5060c403afdee4c15a0edeab69dba0524e2ce592" ||
		got[1] != "4886579a97a96bad83b527862bab409b5a07c741" ||
		got[2] != "00008120-000865D90A10C01E" {
		t.Fatalf("network udids = %v", got)
	}
	for _, u := range got {
		// iOS <16 的 40-hex 必须保持小写原文；带连字符的 iOS 16+ 格式保持上报原样。
		if !strings.Contains(u, "-") && u != strings.ToLower(u) {
			t.Fatalf("iOS<16 UDID must stay lowercase: %q", u)
		}
	}
}

func TestParseUsbmuxNetworkUDIDsMalformed(t *testing.T) {
	if got := parseUsbmuxNetworkUDIDs("not json at all"); got != nil {
		t.Fatalf("malformed should return nil, got %v", got)
	}
}

func TestUnplugSafeFor(t *testing.T) {
	udid := "5952499671171C733D6EF1345D4548A782686804"
	tunnelSet := map[string]bool{"00008120-000865D90A10C01E": true}
	netSet := map[string]bool{udid: true}

	cases := []struct {
		name, udid, ios string
		tunnel, net     map[string]bool
		want            bool
	}{
		{"ios15 with network entry", udid, "15.8.8", tunnelSet, netSet, true},
		{"ios15 without network entry", udid, "15.8.8", tunnelSet, map[string]bool{}, false},
		{"ios18 with tunnel", "00008120-000865D90A10C01E", "18.5", tunnelSet, map[string]bool{}, true},
		{"ios18 without tunnel", "00008120-000865D90A10C01E", "18.5", map[string]bool{}, netSet, false},
		{"unknown ios uses network entry", udid, "", tunnelSet, netSet, true},
		{"empty udid", "", "15.8.8", tunnelSet, netSet, false},
	}
	for _, c := range cases {
		got := unplugSafeFor(c.udid, c.ios, c.tunnel, c.net)
		if got != c.want {
			t.Errorf("%s: unplugSafeFor=%v want %v", c.name, got, c.want)
		}
	}
}
