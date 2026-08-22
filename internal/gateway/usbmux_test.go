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
