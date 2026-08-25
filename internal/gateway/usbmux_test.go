package gateway

import (
	"errors"
	"testing"
)

func stopUSBTunnelsForTest() {
	usbTunnels.mu.Lock()
	defer usbTunnels.mu.Unlock()
	for udid, p := range usbTunnels.procs {
		if p != nil && p.cmd != nil && p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		delete(usbTunnels.procs, udid)
	}
	usbTunnels.misses = map[string]int{}
}

func TestIproxyForwardArgsSameChannelOnBothOS(t *testing.T) {
	usb := iproxyForwardArgs("u1", 8101, 8100, false)
	net := iproxyForwardArgs("u1", 8101, 8100, true)
	if !containsExact(usb, "-u") || !containsExact(usb, "u1") {
		t.Fatalf("usb args=%v", usb)
	}
	if containsExact(usb, "-n") {
		t.Fatalf("USB tunnel must not pass -n: %v", usb)
	}
	if !containsExact(net, "-n") {
		t.Fatalf("Network tunnel must pass -n on this OS too: %v", net)
	}
}

func TestUSBTunnelsToDropAfterEmptyDiscover(t *testing.T) {
	misses := map[string]int{}
	procs := map[string]*usbTunnelProc{
		"u1": {port: 1, done: make(chan struct{})},
		"u2": {port: 2, done: make(chan struct{})},
	}
	if got := usbTunnelsToDrop(nil, nil, procs, misses, 2); len(got) != 0 {
		t.Fatalf("first empty round must keep tunnels, got %v", got)
	}
	if misses["u1"] != 1 || misses["u2"] != 1 {
		t.Fatalf("misses=%v", misses)
	}
	got := usbTunnelsToDrop(nil, nil, procs, misses, 2)
	if len(got) != 2 {
		t.Fatalf("second empty round must drop both, got %v", got)
	}
	if len(misses) != 0 {
		t.Fatalf("dropped ids should clear misses: %v", misses)
	}
}

func TestUSBTunnelsToDropResetsWhenRediscovered(t *testing.T) {
	misses := map[string]int{"u1": 1}
	procs := map[string]*usbTunnelProc{"u1": {port: 1, done: make(chan struct{})}}
	got := usbTunnelsToDrop(map[string]bool{udidKey("u1"): true}, nil, procs, misses, 2)
	if len(got) != 0 {
		t.Fatalf("rediscovered must not drop: %v", got)
	}
	if _, ok := misses["u1"]; ok {
		t.Fatalf("misses should reset: %v", misses)
	}
}

func TestNetworkTunnelsToDropUsesNetSet(t *testing.T) {
	misses := map[string]int{}
	procs := map[string]*usbTunnelProc{
		"u1": {port: 1, network: true, done: make(chan struct{})},
	}
	// u1 不在 netSet：第一轮 miss=1 keep，第二轮 drop
	if got := usbTunnelsToDrop(nil, nil, procs, misses, 2); len(got) != 0 {
		t.Fatalf("first round must keep, got %v", got)
	}
	if misses["u1"] != 1 {
		t.Fatalf("misses=%v", misses)
	}
	got := usbTunnelsToDrop(nil, nil, procs, misses, 2)
	if len(got) != 1 {
		t.Fatalf("second round must drop network tunnel, got %v", got)
	}
	// 在 netSet 中：keep + reset
	misses = map[string]int{}
	procs = map[string]*usbTunnelProc{"u1": {port: 1, network: true, done: make(chan struct{})}}
	if got := usbTunnelsToDrop(nil, map[string]bool{"u1": true}, procs, misses, 2); len(got) != 0 {
		t.Fatalf("network rediscovered must not drop, got %v", got)
	}
}

// TestTransientWDAError 可达性类错误判定。
func TestTransientWDAError(t *testing.T) {
	for _, s := range []string{
		"wda not reachable: wda http 500: Server error",
		"context deadline exceeded (Client.Timeout exceeded while awaiting headers)",
		"dial tcp 127.0.0.1:1: connection refused",
		"read tcp: i/o timeout: timed out",
	} {
		if !transientWDAError(errors.New(s)) {
			t.Errorf("should be transient: %s", s)
		}
	}
	for _, s := range []string{
		"find message input: wda http 404",
		"deep link unsupported and no chat/contact for 861",
		"send unconfirmed",
	} {
		if transientWDAError(errors.New(s)) {
			t.Errorf("should NOT be transient: %s", s)
		}
	}
}

// TestWdaBaseURLForFallback 无隧道时回退 Wi-Fi 地址。
func TestWdaBaseURLForFallback(t *testing.T) {
	if got := wdaBaseURLFor("no-tunnel-udid", "192.168.1.11", 8100, activateViaNetwork); got != "http://192.168.1.11:8100" {
		t.Fatalf("network via uses wifi IP, got %s", got)
	}
	if got := wdaBaseURLFor("no-tunnel-udid", "192.168.1.11", 8100, activateViaUSB); got != "" {
		t.Fatalf("usb via must not use wifi IP, got %s", got)
	}
}

func TestWdaBaseURLForPrefersUSBTunnel(t *testing.T) {
	udid := "aabbccddeeff00112233445566778899aabbccdd"
	usbTunnels.mu.Lock()
	usbTunnels.procs[udid] = &usbTunnelProc{port: 63344, done: make(chan struct{})}
	usbTunnels.mu.Unlock()
	t.Cleanup(func() {
		usbTunnels.mu.Lock()
		delete(usbTunnels.procs, udid)
		usbTunnels.mu.Unlock()
	})
	if got := wdaBaseURLFor(udid, "192.168.10.237", 8100, activateViaUSB); got != "http://127.0.0.1:63344" {
		t.Fatalf("usb via uses usb tunnel, got %s", got)
	}
	if got := wdaBaseURLFor(udid, "192.168.10.237", 8100, activateViaNetwork); got != "http://192.168.10.237:8100" {
		t.Fatalf("network via must ignore usb tunnel, got %s", got)
	}
}

func TestResolveWDABaseURLFallsBackWhenTunnelDead(t *testing.T) {
	udid := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	done := make(chan struct{}) // open: TunnelAddr still lists it
	usbTunnels.mu.Lock()
	usbTunnels.procs[udid] = &usbTunnelProc{port: 1, done: done} // nothing listens on :1
	usbTunnels.mu.Unlock()
	t.Cleanup(func() {
		usbTunnels.mu.Lock()
		delete(usbTunnels.procs, udid)
		usbTunnels.mu.Unlock()
	})
	if got := resolveWDABaseURL(udid, "192.168.10.236", 8100, activateViaUSB); got != "" {
		t.Fatalf("dead USB tunnel must not resolve, got %s", got)
	}
	if got := wdaBaseURLFor(udid, "192.168.10.236", 8100, activateViaUSB); got != "http://127.0.0.1:1" {
		t.Fatalf("usb via still addresses listed USB tunnel, got %s", got)
	}
	if got := wdaBaseURLFor(udid, "192.168.10.236", 8100, activateViaNetwork); got != "http://192.168.10.236:8100" {
		t.Fatalf("network via ignores USB tunnel, got %s", got)
	}
}

func TestTunnelAddrNormalizesHyphenUDID(t *testing.T) {
	key := normalizeUDID("00008120-001a2b3c4d5e6f70")
	done := make(chan struct{})
	usbTunnels.mu.Lock()
	usbTunnels.procs[key] = &usbTunnelProc{port: 61234, done: done}
	usbTunnels.mu.Unlock()
	t.Cleanup(func() {
		usbTunnels.mu.Lock()
		delete(usbTunnels.procs, key)
		usbTunnels.mu.Unlock()
	})
	if got := TunnelAddr("00008120-001a2b3c4d5e6f70"); got != "127.0.0.1:61234" {
		t.Fatalf("lowercase lookup missed normalized tunnel: %q", got)
	}
}
