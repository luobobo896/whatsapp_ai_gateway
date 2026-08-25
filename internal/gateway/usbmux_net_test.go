package gateway

import "testing"

func TestBuildUsbmuxNetStatusStates(t *testing.T) {
	devices := []Device{
		{UDID: "a", Name: "Wireless", IP: "10.0.0.1"},
		{UDID: "b", Name: "UsbOnly", IP: "10.0.0.2"},
		{UDID: "c", Name: "Absent"},
	}
	conns := usbmuxConnSets{
		netSet: map[string]bool{"A": true},
		usbSet: map[string]bool{"A": true, "B": true},
	}
	st := buildUsbmuxNetStatus(devices, conns, true)
	if st.Total != 2 || st.Network != 1 || st.UsbOnly != 1 || st.Absent != 0 || st.UnplugSafe != 1 {
		t.Fatalf("counts wrong: total=%d net=%d usb=%d absent=%d unplug=%d",
			st.Total, st.Network, st.UsbOnly, st.Absent, st.UnplugSafe)
	}
	if !st.NeedsRepair {
		t.Fatal("NeedsRepair should be true (B has USB but no Network)")
	}
	if !st.AutoRepair {
		t.Fatal("AutoRepair should pass through")
	}
	if st.Devices[0].Connection != "Network" || st.Devices[1].Connection != "UsbOnly" {
		t.Fatalf("sort order wrong: %v %v", st.Devices[0].Connection, st.Devices[1].Connection)
	}
	if st.Devices[0].UnplugSafe != true || st.Devices[1].UnplugSafe != false {
		t.Fatalf("unplug_safe flags wrong: %v", st.Devices)
	}
}

func TestBuildUsbmuxNetStatusUnpluggedNotCounted(t *testing.T) {
	devices := []Device{{UDID: "a", Name: "Gone"}}
	st := buildUsbmuxNetStatus(devices, usbmuxConnSets{netSet: map[string]bool{}, usbSet: map[string]bool{}}, true)
	if st.Total != 0 || len(st.Devices) != 0 || st.Network != 0 {
		t.Fatalf("unplugged devices must not remain in 0/1: %+v", st)
	}
}

func TestBuildUsbmuxNetStatusAllNetworkNoRepair(t *testing.T) {
	devices := []Device{{UDID: "a"}, {UDID: "B"}}
	conns := usbmuxConnSets{
		netSet: map[string]bool{"A": true, "B": true},
		usbSet: map[string]bool{"A": true, "B": true},
	}
	st := buildUsbmuxNetStatus(devices, conns, false)
	if st.NeedsRepair {
		t.Fatal("NeedsRepair should be false when all Network")
	}
	if st.UnplugSafe != 2 {
		t.Fatalf("unplug_safe should be 2, got %d", st.UnplugSafe)
	}
}

func TestUsbmuxNetNeedsRepair(t *testing.T) {
	if usbmuxNetNeedsRepair(usbmuxConnSets{
		netSet: map[string]bool{"A": true}, usbSet: map[string]bool{"A": true},
	}) {
		t.Fatal("should not need repair")
	}
	if !usbmuxNetNeedsRepair(usbmuxConnSets{
		netSet: map[string]bool{}, usbSet: map[string]bool{"A": true},
	}) {
		t.Fatal("should need repair (USB only, no Network)")
	}
}

func TestParseUsbmuxConnectionTypesNetworkWins(t *testing.T) {
	raw := `{"deviceList":[
		{"Udid":"a","ConnectionType":"Network"},
		{"Udid":"a","ConnectionType":"USB"},
		{"Udid":"b","ConnectionType":"USB"}
	]}`
	got := parseUsbmuxConnectionTypes(raw)
	if got["A"] != "Network" {
		t.Fatalf("A should be Network (Network must win over USB), got %q", got["A"])
	}
	if got["B"] != "USB" {
		t.Fatalf("B should be USB, got %q", got["B"])
	}
}
