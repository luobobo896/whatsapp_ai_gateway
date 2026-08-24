package main

import (
	"testing"
	"time"
)

func TestPickDevicePrefersNetwork(t *testing.T) {
	udid := "4886579a97a96bad83b527862bab409b5a07c741"
	devs := []muxDevice{
		{ID: 13, UDID: "5952499671171c733d6ef1345d4548a782686804", Type: "USB"},
		{ID: 12, UDID: udid, Type: "Network"},
		{ID: 9, UDID: udid, Type: "USB"},
	}
	got, ok := pickDevice(devs, udid)
	if !ok || got.ID != 12 || got.Type != "Network" {
		t.Fatalf("got %#v ok=%v", got, ok)
	}
}

func TestPickDeviceUSBFallback(t *testing.T) {
	udid := "5952499671171c733d6ef1345d4548a782686804"
	devs := []muxDevice{
		{ID: 13, UDID: udid, Type: "USB"},
		{ID: 9, UDID: "4886579a97a96bad83b527862bab409b5a07c741", Type: "USB"},
	}
	got, ok := pickDevice(devs, udid)
	if !ok || got.ID != 13 || got.Type != "USB" {
		t.Fatalf("got %#v ok=%v", got, ok)
	}
}

func TestPickDeviceMissing(t *testing.T) {
	if _, ok := pickDevice(nil, "abc"); ok {
		t.Fatal("expected missing")
	}
}

func TestMuxPortToTCP(t *testing.T) {
	if muxPortToTCP(32498) != 62078 {
		t.Fatalf("lockdown tcp=%d", muxPortToTCP(32498))
	}
	if muxPortToTCP(40445) != 64925 {
		t.Fatalf("40445 -> %d want 64925", muxPortToTCP(40445))
	}
}

func TestWaitPreferNetworkEventually(t *testing.T) {
	udid := "4886579a97a96bad83b527862bab409b5a07c741"
	n := 0
	list := func() []muxDevice {
		n++
		if n < 3 {
			return []muxDevice{{ID: 9, UDID: udid, Type: "USB"}}
		}
		return []muxDevice{
			{ID: 9, UDID: udid, Type: "USB"},
			{ID: 12, UDID: udid, Type: "Network"},
		}
	}
	got, ok := waitPreferNetwork(udid, 2*time.Second, list)
	if !ok || got.Type != "Network" || got.ID != 12 {
		t.Fatalf("got %#v ok=%v after %d polls", got, ok, n)
	}
}

func TestWaitPreferNetworkUSBFallback(t *testing.T) {
	udid := "5952499671171c733d6ef1345d4548a782686804"
	list := func() []muxDevice {
		return []muxDevice{{ID: 13, UDID: udid, Type: "USB"}}
	}
	got, ok := waitPreferNetwork(udid, 200*time.Millisecond, list)
	if !ok || got.Type != "USB" {
		t.Fatalf("got %#v ok=%v", got, ok)
	}
	if err := requireMuxNetwork(udid, got, ok); err == nil {
		t.Fatal("USB-only is not Network (unplug unsafe)")
	}
}

func TestRequireMuxNetwork(t *testing.T) {
	udid := "5060c403afdee4c15a0edeab69dba0524e2ce592"
	if err := requireMuxNetwork(udid, muxDevice{}, false); err == nil {
		t.Fatal("missing device must fail")
	}
	if err := requireMuxNetwork(udid, muxDevice{ID: 1, Type: "USB"}, true); err == nil {
		t.Fatal("USB must fail")
	}
	if err := requireMuxNetwork(udid, muxDevice{ID: 2, Type: "Network"}, true); err != nil {
		t.Fatal(err)
	}
}
