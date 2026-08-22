package gateway

import (
	"os"
	"sync"
	"testing"
	"time"
)

// TestProcessTaskRejectsOfflineDevice 离线设备（WDA 不可达）任务被立即剔除：
// 全部明细标 failed 且错误含「设备离线」，汇总状态为 device_unreachable。
func TestProcessTaskRejectsOfflineDevice(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Devices: []Device{{UDID: "udid-off", IP: "127.0.0.1", Port: 1}}}
	e := NewExecutor(cfg, nil, nil, dir)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range e.ReportQ {
		}
	}()
	go func() {
		defer wg.Done()
		for range e.SummaryQ {
		}
	}()

	items := []TaskItem{
		{ItemID: "i1", Phone: "8613800000001", Seq: 1},
		{ItemID: "i2", Phone: "8613800000002", Seq: 2},
	}
	e.Submit(TaskDispatch{TaskID: "task-off", UDID: "udid-off", Items: items})

	deadline := time.After(15 * time.Second)
	for {
		m := e.readItems("task-off")
		if len(m) == 2 {
			for id, r := range m {
				if r.Status != "failed" {
					t.Fatalf("item %s status = %s, want failed", id, r.Status)
				}
				if !contains(r.Error, "设备离线") {
					t.Fatalf("item %s error missing offline reason: %s", id, r.Error)
				}
			}
			s := e.readSummary("task-off")
			if s == nil || s.Status != taskStopUnreach || s.SentFail != 2 || s.Pending != 0 {
				t.Fatalf("summary wrong: %+v", s)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("items not rejected in time: %+v", m)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// TestParseCoordReply 视觉模型坐标回复解析（逗号/中文逗号/x 分隔）。
func TestParseCoordReply(t *testing.T) {
	cases := []struct {
		in      string
		x, y    int
		wantErr bool
	}{
		{"123,456", 123, 456, false},
		{"x=100，y=200", 100, 200, false},
		{"  88 x 240 ", 88, 240, false},
		{"NONE", 0, 0, true},
	}
	for _, c := range cases {
		x, y, err := parseCoordReply(c.in, "test")
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: want error", c.in)
			}
			continue
		}
		if err != nil || x != c.x || y != c.y {
			t.Errorf("%q: got (%d,%d,%v), want (%d,%d,nil)", c.in, x, y, err, c.x, c.y)
		}
	}
}

// TestResendPersistedFieldsComplete 补报上行的明细字段完整：
// 新记录带 serial/device_name 落盘后原样补报；老记录缺字段时按 udid 从配置兜底补全。
func TestResendPersistedFieldsComplete(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Devices: []Device{{UDID: "udid-r2", Serial: "SER-R2", Name: "iPhone R2"}}}
	// 老格式记录：无 serial/device_name（须在构造前就位——构造时一次性迁移进 SQLite）。
	_ = os.WriteFile(dir+"/task-old.json", []byte(`{"i2":{"phone":"8613800000002","status":"sent","udid":"udid-r2","conn_type":"wifi"}}`), 0o600)
	e := NewExecutor(cfg, nil, nil, dir)

	// 新记录：带完整字段落盘。
	e.finishItem(ItemResult{TaskID: "task-r", ItemID: "i1", Phone: "8613800000001", Status: "sent",
		Udid: "udid-r2", Serial: "SER-R2", DeviceName: "iPhone R2", ConnType: "usb"})

	e.ResendPersisted()
	got := map[string]ItemResult{}
	for len(got) < 2 {
		select {
		case r := <-e.ReportQ:
			got[r.ItemID] = r
		case <-time.After(3 * time.Second):
			t.Fatalf("resend incomplete: %+v", got)
		}
	}
	if r := got["i1"]; r.Serial != "SER-R2" || r.DeviceName != "iPhone R2" || r.ConnType != "usb" {
		t.Fatalf("i1 fields lost: %+v", r)
	}
	if r := got["i2"]; r.Serial != "SER-R2" || r.DeviceName != "iPhone R2" {
		t.Fatalf("i2 not enriched from config: %+v", r)
	}
}


// TestCancelIdempotent 重复 task:cancel 同一 task 不得 panic（平台重推 cancel 常见）。
func TestCancelIdempotent(t *testing.T) {
	e := NewExecutor(&Config{Devices: []Device{{UDID: "udid-c", IP: ""}}}, nil, nil, t.TempDir())
	// 人为放入 cancel 通道，模拟已 Submit 尚未跑完。
	ch := make(chan struct{})
	e.mu.Lock()
	e.cancel["task-c"] = ch
	e.mu.Unlock()
	e.Cancel("task-c")
	e.Cancel("task-c") // 第二次不得 panic
	e.Cancel("missing")
	select {
	case <-ch:
	default:
		t.Fatal("cancel channel should be closed")
	}
}

// TestIsUnreachableItemError 失联判定：真实传输类命中，裸 "connection" 业务文案不命中。
func TestIsUnreachableItemError(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"wda not reachable: dial tcp: i/o timeout", true},
		{"wda not reachable: context deadline exceeded", true},
		{"Get http://x: Client.Timeout exceeded while awaiting headers", true},
		{"connection refused", true},
		{"connection reset by peer", true},
		{"no connection to chat server", false}, // 裸 connection 不应整单中止
		{"element timed out waiting", false},
		{"聊天列表未找到好友会话", false},
	}
	for _, c := range cases {
		if got := isUnreachableItemError(c.in); got != c.want {
			t.Fatalf("%q: got %v want %v", c.in, got, c.want)
		}
	}
}
