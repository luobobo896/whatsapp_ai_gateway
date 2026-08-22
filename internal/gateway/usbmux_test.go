package gateway

import (
	"errors"
	"testing"
)

func TestUSBTunnelsToDropAfterEmptyDiscover(t *testing.T) {
	misses := map[string]int{}
	tunnels := []string{"u1", "u2"}
	if got := usbTunnelsToDrop(nil, tunnels, misses, 2); len(got) != 0 {
		t.Fatalf("first empty round must keep tunnels, got %v", got)
	}
	if misses["u1"] != 1 || misses["u2"] != 1 {
		t.Fatalf("misses=%v", misses)
	}
	got := usbTunnelsToDrop(nil, tunnels, misses, 2)
	if len(got) != 2 {
		t.Fatalf("second empty round must drop both, got %v", got)
	}
	if len(misses) != 0 {
		t.Fatalf("dropped ids should clear misses: %v", misses)
	}
}

func TestUSBTunnelsToDropResetsWhenRediscovered(t *testing.T) {
	misses := map[string]int{"u1": 1}
	got := usbTunnelsToDrop(map[string]bool{"u1": true}, []string{"u1"}, misses, 2)
	if len(got) != 0 {
		t.Fatalf("rediscovered must not drop: %v", got)
	}
	if _, ok := misses["u1"]; ok {
		t.Fatalf("misses should reset: %v", misses)
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
	if got := wdaBaseURLFor("no-tunnel-udid", "192.168.1.11", 8100); got != "http://192.168.1.11:8100" {
		t.Fatalf("fallback wrong: %s", got)
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
	if got := wdaBaseURLFor(udid, "192.168.10.237", 8100); got != "http://127.0.0.1:63344" {
		t.Fatalf("tunnel first, got %s", got)
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
	// 死隧道不得挡住 Wi-Fi 地址选择（探活由调用方再做；这里只验证回退选址）。
	got := resolveWDABaseURL(udid, "192.168.10.236", 8100)
	if got != "http://192.168.10.236:8100" {
		t.Fatalf("want wifi fallback, got %s", got)
	}
	if got := wdaBaseURLFor(udid, "192.168.10.236", 8100); got != "http://127.0.0.1:1" {
		t.Fatalf("wdaBaseURLFor still prefers listed tunnel, got %s", got)
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
