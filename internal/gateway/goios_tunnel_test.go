package gateway

import (
	"strings"
	"testing"
)

func TestGoiosTunnelStartArgsUserspace(t *testing.T) {
	args := goiosTunnelStartArgs(true)
	joined := strings.Join(args, " ")
	if !containsExact(args, "tunnel") || !containsExact(args, "start") || !containsExact(args, "--userspace") {
		t.Fatalf("userspace args: %s", joined)
	}
	if !containsExact(args, "--tunnel-info-port=28100") {
		t.Fatalf("missing info port: %s", joined)
	}
	kernel := goiosTunnelStartArgs(false)
	if containsExact(kernel, "--userspace") {
		t.Fatalf("kernel args must not set userspace: %v", kernel)
	}
}

func TestParseGoIOSTunnelList(t *testing.T) {
	raw := `[{"address":"fde1::2","rsdPort":59124,"udid":"00008120-000865D90A10C01E","userspaceTun":true,"userspaceTunPort":60105}]`
	infos, err := parseGoIOSTunnelList(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !tunnelHasDevice(infos, "00008120-000865D90A10C01E") {
		t.Fatalf("missing device: %#v", infos)
	}
	if tunnelHasDevice(infos, "5060c403afdee4c15a0edeab69dba0524e2ce592") {
		t.Fatal("old udid must not match")
	}
}

func TestParseGoIOSTunnelListWrapped(t *testing.T) {
	raw := `{"tunnels":[{"udid":"AAA","address":"::1","rsdPort":1}]}`
	infos, err := parseGoIOSTunnelList(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !tunnelHasDevice(infos, "aaa") {
		t.Fatalf("%#v", infos)
	}
}

func TestWithGoIOSTunnelPort(t *testing.T) {
	got := withGoIOSTunnelPort([]string{"--udid=U", "runwda"})
	if got[0] != "--tunnel-info-port=28100" || got[1] != "--udid=U" {
		t.Fatalf("%v", got)
	}
}

func TestTunnelHasDeviceRequiresEndpoint(t *testing.T) {
	if tunnelHasDevice([]goiosTunnelInfo{{Udid: "U"}}, "U") {
		t.Fatal("empty address/port is not ready")
	}
	if !tunnelHasDevice([]goiosTunnelInfo{{Udid: "U", UserspaceTUNPort: 9}}, "U") {
		t.Fatal("userspace port should count")
	}
}
