package gateway

import (
	"strings"
	"testing"
)

func TestPlanForIOSVersionGates(t *testing.T) {
	cases := []struct {
		ver                string
		ddi, tun, dev, usp bool
	}{
		{"15.8.7", true, false, false, true},
		{"16.4.1", true, false, true, true},
		{"16.7", true, false, true, true},
		{"17.0", false, true, true, false},
		{"17.3.1", false, true, true, false},
		{"17.4", false, true, true, true},
		{"18.6.1", false, true, true, true},
		{"26.0", false, true, true, true},
		{"", false, false, false, true},
	}
	for _, c := range cases {
		p := planForIOS(c.ver)
		if p.NeedDDI != c.ddi || p.NeedTunnel != c.tun || p.NeedDevMode != c.dev || p.UserspaceOK != c.usp {
			t.Fatalf("%s: ddi=%v tun=%v dev=%v usp=%v want %v %v %v %v",
				c.ver, p.NeedDDI, p.NeedTunnel, p.NeedDevMode, p.UserspaceOK, c.ddi, c.tun, c.dev, c.usp)
		}
	}
}

func TestLikelyNeedsTunnel(t *testing.T) {
	if likelyNeedsTunnel("5060c403afdee4c15a0edeab69dba0524e2ce592", "15.8.7") {
		t.Fatal("iOS 15 40-hex must not need tunnel")
	}
	if !likelyNeedsTunnel("00008120-000865D90A10C01E", "18.1") {
		t.Fatal("iOS 18 must need tunnel")
	}
	if !likelyNeedsTunnel("00008120-000865D90A10C01E", "") {
		t.Fatal("unknown version + new UDID should start tunnel daemon")
	}
	if likelyNeedsTunnel("5060c403afdee4c15a0edeab69dba0524e2ce592", "") {
		t.Fatal("unknown version + old UDID should not assume tunnel")
	}
}

func TestParseDevModeEnabled(t *testing.T) {
	on, ok := parseDevModeEnabled(`{"enabled": true}`)
	if !ok || !on {
		t.Fatalf("enabled true: %v %v", on, ok)
	}
	off, ok := parseDevModeEnabled(`{"enabled":false}`)
	if !ok || off {
		t.Fatalf("enabled false: %v %v", off, ok)
	}
	on, ok = parseDevModeEnabled("true")
	if !ok || !on {
		t.Fatal("bare true")
	}
	if _, ok := parseDevModeEnabled(""); ok {
		t.Fatal("empty must not parse")
	}
}

func TestParseGoIOSDeviceListVersion(t *testing.T) {
	raw := `{"deviceList":[{"Udid":"5060c403afdee4c15a0edeab69dba0524e2ce592","ProductVersion":"15.8.7"}]}`
	got := parseGoIOSDeviceListVersion(raw, "5060c403afdee4c15a0edeab69dba0524e2ce592")
	if got != "15.8.7" {
		t.Fatalf("got %q", got)
	}
}

func TestRememberIOSVersion(t *testing.T) {
	udid := "TEST-UDID-CACHE-1"
	rememberIOSVersion(udid, "18.2")
	if cachedIOSVersion(udid) != "18.2" {
		t.Fatal(cachedIOSVersion(udid))
	}
	if resolveIOSVersion(udid, "18.2") != "18.2" {
		t.Fatal("persisted")
	}
}

func TestGoiosImageAutoArgs(t *testing.T) {
	args := goiosImageAutoArgs("U1", "/tmp/ddi")
	joined := strings.Join(args, " ")
	for _, w := range []string{"--udid=U1", "image", "auto", "--basedir=/tmp/ddi"} {
		if !containsExact(args, w) {
			t.Fatalf("missing %s in %s", w, joined)
		}
	}
}
