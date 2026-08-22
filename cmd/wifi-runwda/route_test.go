package main

import "testing"

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
