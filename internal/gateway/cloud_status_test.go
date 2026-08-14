package gateway

import "testing"

func newStatusTestGateway(t *testing.T) (*Gateway, *WDAManager, *Executor) {
	t.Helper()
	cfg := &Config{}
	wdaMgr := NewWDAManager("", "")
	exec := NewExecutor(cfg, wdaMgr, nil, t.TempDir())
	g := New(cfg, wdaMgr, exec, nil, nil)
	return g, wdaMgr, exec
}

func setWDA(t *testing.T, m *WDAManager, udid string, running bool) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if running {
		m.processes[udid] = &wdaProc{done: make(chan struct{})}
	} else {
		delete(m.processes, udid)
	}
}

func TestDeviceCloudStatus(t *testing.T) {
	g, wdaMgr, exec := newStatusTestGateway(t)

	healthy := &Device{UDID: "u1", LastHealth: map[string]any{"ok": true}}
	unhealthy := &Device{UDID: "u2", LastHealth: map[string]any{"ok": false}}

	cases := []struct {
		name    string
		dev     *Device
		usb     bool
		running bool
		busy    bool
		want    string
	}{
		{name: "running and healthy", dev: healthy, usb: false, running: true, want: "online"},
		{name: "running and usb but unhealthy", dev: unhealthy, usb: true, running: true, want: "online"},
		{name: "running but offline", dev: unhealthy, usb: false, running: true, want: "offline"},
		{name: "healthy but wda not running", dev: healthy, usb: false, running: false, want: "offline"},
		{name: "usb but wda not running", dev: healthy, usb: true, running: false, want: "offline"},
		{name: "busy wins", dev: healthy, usb: true, running: true, busy: true, want: "busy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setWDA(t, wdaMgr, tc.dev.UDID, tc.running)
			exec.mu.Lock()
			exec.busy[tc.dev.UDID] = tc.busy
			exec.mu.Unlock()
			if got := g.deviceCloudStatus(tc.dev, tc.usb); got != tc.want {
				t.Fatalf("deviceCloudStatus = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDeviceListReportUsesRunningState(t *testing.T) {
	g, wdaMgr, _ := newStatusTestGateway(t)
	g.Cfg.Devices = []Device{
		{UDID: "u-online", IP: "192.168.20.1", LastHealth: map[string]any{"ok": true}},
		{UDID: "u-stale", IP: "192.168.20.2", LastHealth: map[string]any{"ok": true}},
	}
	setWDA(t, wdaMgr, "u-online", true)
	setWDA(t, wdaMgr, "u-stale", false)

	got := g.deviceListReport()
	status := map[string]string{}
	for _, d := range got {
		status[d["udid"].(string)] = d["wda_status"].(string)
	}
	if status["u-online"] != "online" {
		t.Fatalf("u-online status = %q, want online", status["u-online"])
	}
	if status["u-stale"] != "offline" {
		t.Fatalf("u-stale status = %q, want offline (WDA 进程未运行，即使健康探活 ok 也不上报 online)", status["u-stale"])
	}
}
