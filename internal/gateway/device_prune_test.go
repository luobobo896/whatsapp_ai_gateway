package gateway

import "testing"

func TestDeviceDeletable(t *testing.T) {
	cases := []struct {
		name               string
		busy, healthy, run bool
		want               bool
	}{
		{name: "unactivated can delete", want: true},
		{name: "healthy cannot delete", healthy: true, want: false},
		{name: "running cannot delete", run: true, want: false},
		{name: "busy cannot delete", busy: true, want: false},
		{name: "busy and healthy cannot delete", busy: true, healthy: true, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deviceDeletable(tc.busy, tc.healthy, tc.run); got != tc.want {
				t.Fatalf("deviceDeletable = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPurgeRejectsActivatedAndBusy(t *testing.T) {
	g, wdaMgr, exec := newStatusTestGateway(t)
	g.Cfg.Devices = []Device{
		{UDID: "u-on", LastHealth: map[string]any{"ok": true}},
		{UDID: "u-busy", LastHealth: map[string]any{"ok": false}},
		{UDID: "u-off", LastHealth: map[string]any{"ok": false}},
	}
	setWDA(t, wdaMgr, "u-on", false)
	setWDA(t, wdaMgr, "u-busy", false)
	setWDA(t, wdaMgr, "u-off", false)
	exec.mu.Lock()
	exec.busy["u-busy"] = true
	exec.mu.Unlock()

	if _, _, err := g.purgeUnactivatedDevice("u-on"); err != errDeviceActivated {
		t.Fatalf("activated delete err = %v, want errDeviceActivated", err)
	}
	if g.Cfg.Device("u-on") == nil {
		t.Fatal("activated device must stay")
	}
	if _, _, err := g.purgeUnactivatedDevice("u-busy"); err != errDeviceBusy {
		t.Fatalf("busy delete err = %v, want errDeviceBusy", err)
	}
	removed, _, err := g.purgeUnactivatedDevice("u-off")
	if err != nil || !removed {
		t.Fatalf("unactivated delete: removed=%v err=%v", removed, err)
	}
	if g.Cfg.Device("u-off") != nil {
		t.Fatal("unactivated device must be removed")
	}
}

func TestPurgeAllowedAfterStop(t *testing.T) {
	g, wdaMgr, _ := newStatusTestGateway(t)
	g.Cfg.Devices = []Device{{UDID: "u1", AutoReactivate: true, LastHealth: map[string]any{"ok": true}}}
	setWDA(t, wdaMgr, "u1", true)
	if _, _, err := g.purgeUnactivatedDevice("u1"); err != errDeviceActivated {
		t.Fatalf("before stop err = %v", err)
	}
	setWDA(t, wdaMgr, "u1", false)
	applyHealth(&g.Cfg.Devices[0], WDAHealth{OK: false, Error: "stopped"})
	g.Cfg.Devices[0].AutoReactivate = false
	removed, _, err := g.purgeUnactivatedDevice("u1")
	if err != nil || !removed {
		t.Fatalf("after stop: removed=%v err=%v", removed, err)
	}
}

func TestPurgeClearsRuntimeAndMetrics(t *testing.T) {
	g, _, exec := newStatusTestGateway(t)
	g.Cfg.Devices = []Device{{UDID: "u-off", LastHealth: map[string]any{"ok": false}}}
	g.rememberSerial("u-off", "SERIAL1")
	g.rememberCloudStatus("u-off", "offline")
	exec.recordMetric("u-off", "t1", "failed", false)
	if exec.Metrics("u-off").SentFail != 1 {
		t.Fatal("setup metrics failed")
	}

	if _, _, err := g.purgeUnactivatedDevice("u-off"); err != nil {
		t.Fatal(err)
	}
	if g.SerialOf("u-off") != "" {
		t.Fatalf("serial cache leftover: %q", g.SerialOf("u-off"))
	}
	g.statusMu.Lock()
	_, ok := g.lastStatus["u-off"]
	g.statusMu.Unlock()
	if ok {
		t.Fatal("cloud status cache leftover")
	}
	if exec.Metrics("u-off").SentFail != 0 || exec.Metrics("u-off").Total != 0 {
		t.Fatalf("metrics leftover: %+v", exec.Metrics("u-off"))
	}
}

func TestDeviceAbsent(t *testing.T) {
	cases := []struct {
		name                         string
		attached, healthy, busy, run bool
		want                         bool
	}{
		{name: "usb attached keeps device", attached: true, want: false},
		{name: "wifi healthy keeps device", healthy: true, want: false},
		{name: "busy keeps device", busy: true, want: false},
		{name: "activating keeps device", run: true, want: false},
		{name: "offline is absent", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deviceAbsent(tc.attached, tc.healthy, tc.busy, tc.run); got != tc.want {
				t.Fatalf("deviceAbsent = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPruneOfflineDevicesRemovesOnlyAbsent(t *testing.T) {
	g, wdaMgr, exec := newStatusTestGateway(t)
	g.Cfg.Devices = []Device{
		{UDID: "u-usb", LastHealth: map[string]any{"ok": false}},
		{UDID: "u-wifi", LastHealth: map[string]any{"ok": true}},
		{UDID: "u-busy", LastHealth: map[string]any{"ok": false}},
		{UDID: "u-run", LastHealth: map[string]any{"ok": false}},
		{UDID: "u-gone", LastHealth: map[string]any{"ok": false}},
	}
	exec.mu.Lock()
	exec.busy["u-busy"] = true
	exec.mu.Unlock()
	setWDA(t, wdaMgr, "u-run", true)

	gone := g.pruneOfflineDevices(map[string]bool{"u-usb": true})
	if len(gone) != 1 || gone[0] != "u-gone" {
		t.Fatalf("pruned = %v, want [u-gone]", gone)
	}
	if g.Cfg.Device("u-gone") != nil {
		t.Fatal("offline device still in config")
	}
	for _, u := range []string{"u-usb", "u-wifi", "u-busy", "u-run"} {
		if g.Cfg.Device(u) == nil {
			t.Fatalf("%s must stay", u)
		}
	}
}

func TestDeviceListHidesOfflineConfigured(t *testing.T) {
	t.Cleanup(stopUSBTunnelsForTest)
	g, wdaMgr, _ := newStatusTestGateway(t)
	g.Cfg.Devices = []Device{
		{UDID: "u-offline-test", LastHealth: map[string]any{"ok": false}},
		{UDID: "u-online-test", LastHealth: map[string]any{"ok": true}},
	}
	setWDA(t, wdaMgr, "u-offline-test", false)
	setWDA(t, wdaMgr, "u-online-test", false)

	got := g.deviceList()
	seen := map[string]bool{}
	for _, d := range got {
		udid, _ := d["udid"].(string)
		seen[udid] = true
	}
	if seen["u-offline-test"] {
		t.Fatal("offline configured device must not appear in list")
	}
	if !seen["u-online-test"] {
		t.Fatal("healthy Wi-Fi device must appear")
	}
	if g.Cfg.Device("u-offline-test") == nil {
		t.Fatal("list filter must not delete until watchdog prune")
	}
	for _, d := range got {
		if d["udid"] == "u-online-test" && d["deletable"] != false {
			t.Fatalf("activated device deletable = %v, want false", d["deletable"])
		}
	}
}

func TestOpenConfigDropsLegacyIgnored(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.writeKey("ignored", []string{"dead-udid"}); err != nil {
		t.Fatal(err)
	}
	c.Close()

	c2, err := OpenConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if _, ok, err := c2.readKey("ignored"); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("legacy ignored key must be deleted")
	}
}
