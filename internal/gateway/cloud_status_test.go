package gateway

import (
	"testing"
	"time"
)

func newStatusTestGateway(t *testing.T) (*Gateway, *WDAManager, *Executor) {
	t.Helper()
	cfg := &Config{}
	wdaMgr := NewWDAManager("", "", "")
	exec := NewExecutor(cfg, wdaMgr, nil, t.TempDir())
	g := New(cfg, wdaMgr, exec, nil, nil)
	return g, wdaMgr, exec
}

func setWDA(t *testing.T, m *WDAManager, udid string, running bool) {
	t.Helper()
	setWDAStarted(t, m, udid, running, 10*time.Minute)
}

// setWDAStarted 同 setWDA，可指定进程已运行时长（激活宽限期测试用）。
func setWDAStarted(t *testing.T, m *WDAManager, udid string, running bool, age time.Duration) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if running {
		m.processes[udid] = &wdaProc{done: make(chan struct{})}
		m.startedAt[udid] = time.Now().Add(-age)
	} else {
		delete(m.processes, udid)
		delete(m.startedAt, udid)
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
		{name: "healthy without host process", dev: healthy, usb: false, running: false, want: "online"},
		{name: "usb and healthy without host process", dev: healthy, usb: true, running: false, want: "online"},
		{name: "usb unhealthy without host process", dev: unhealthy, usb: true, running: false, want: "offline"},
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

// TestDeviceCloudStatusStartGrace 激活宽限期内（进程刚拉起、监听/隧道未就绪）
// 不判 offline，避免「激活成功→短暂离线→又在线」抖动；宽限期外恢复真实判定。
func TestDeviceCloudStatusStartGrace(t *testing.T) {
	g, wdaMgr, _ := newStatusTestGateway(t)
	dev := &Device{UDID: "u-grace", LastHealth: map[string]any{"ok": false}}

	setWDAStarted(t, wdaMgr, dev.UDID, true, 5*time.Second)
	if got := g.deviceCloudStatus(dev, false); got != "online" {
		t.Fatalf("grace period should be online, got %q", got)
	}
	setWDAStarted(t, wdaMgr, dev.UDID, true, 10*time.Minute)
	if got := g.deviceCloudStatus(dev, false); got != "offline" {
		t.Fatalf("after grace should be offline, got %q", got)
	}
}

func TestDeviceListReportUsesRunningState(t *testing.T) {
	g, wdaMgr, _ := newStatusTestGateway(t)
	g.Cfg.Devices = []Device{
		{UDID: "u-online", Serial: "C39ST1KEHG00", IP: "192.168.20.1", LastHealth: map[string]any{"ok": true}},
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
	if got[0]["serial"] != "C39ST1KEHG00" {
		t.Fatalf("u-online serial = %v, want C39ST1KEHG00", got[0]["serial"])
	}
	if status["u-stale"] != "online" {
		t.Fatalf("u-stale status = %q, want online (机上 /status 通了即可，主机激活进程可以已因拔 USB 退出)", status["u-stale"])
	}
}

func TestWDAAppearsRunningFromHealth(t *testing.T) {
	if !wdaAppearsRunning(true, nil) {
		t.Fatal("host process still starting must count as running")
	}
	if !wdaAppearsRunning(false, map[string]any{"ok": true}) {
		t.Fatal("healthy Wi-Fi WDA must count as running after USB unplug")
	}
	if wdaAppearsRunning(false, map[string]any{"ok": false}) {
		t.Fatal("dead host and failed health must not look running")
	}
}

func TestWDACloudStatusTunnelCountsAsAttached(t *testing.T) {
	// USB 枚举空、last_health 失败时，只要隧道还在就应上报 online，否则平台只给一台下发。
	if !attachedUSB("u1", nil, true) {
		t.Fatal("tunnel-only device must count as USB attached")
	}
	if attachedUSB("u1", nil, false) {
		t.Fatal("no discover and no tunnel")
	}
	if !attachedUSB("u1", map[string]bool{"u1": true}, false) {
		t.Fatal("discover hit must count")
	}
	if got := wdaCloudStatus(false, true, false, true, false); got != "online" {
		t.Fatalf("running + attached + unhealthy = %q, want online", got)
	}
	if got := wdaCloudStatus(false, true, false, false, false); got != "offline" {
		t.Fatalf("running + no attach + unhealthy = %q, want offline", got)
	}
}

func TestDeviceListReportSkipsIgnored(t *testing.T) {
	g, wdaMgr, _ := newStatusTestGateway(t)
	g.Cfg.Devices = []Device{
		{UDID: "u-keep", LastHealth: map[string]any{"ok": true}},
		{UDID: "u-hidden", LastHealth: map[string]any{"ok": true}},
	}
	g.Cfg.Ignored = []string{"u-hidden"}
	setWDA(t, wdaMgr, "u-keep", true)
	setWDA(t, wdaMgr, "u-hidden", true)

	got := g.deviceListReport()
	if len(got) != 1 || got[0]["udid"] != "u-keep" {
		t.Fatalf("report = %+v, want only u-keep", got)
	}
}
