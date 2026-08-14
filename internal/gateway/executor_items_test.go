package gateway

import (
	"strings"
	"testing"
	"time"
)

// TestDeviceItemsGrouping /api/items 按设备分组：带 udid 的新格式直接归组；
// 旧格式按 metrics 的 batch_id→udid 尽力归因；仍无法归因的进「未知设备」组。
func TestDeviceItemsGrouping(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Devices: []Device{
		{UDID: "udid-1", Serial: "SER1", Name: "运营机-01"},
		{UDID: "udid-2", Serial: "SER2", Name: "运营机-02"},
	}}
	e := NewExecutor(cfg, nil, nil, dir)
	e.now = func() time.Time { return time.Date(2026, 8, 14, 12, 0, 0, 0, time.Local) }

	// 新格式：明细自带 udid。
	e.finishItem(ItemResult{TaskID: "task-a", ItemID: "a1", Phone: "8613800000001", Status: "sent", Udid: "udid-1", SentAt: "2026-08-14T10:00:00+08:00"})
	e.finishItem(ItemResult{TaskID: "task-a", ItemID: "a2", Phone: "8613800000002", Status: "sent", Udid: "udid-1", SentAt: "2026-08-14T10:05:00+08:00"})
	// 旧格式：无 udid，但 metrics.json 记了该设备最近任务 batch_id，可归因。
	e.finishItem(ItemResult{TaskID: "task-b", ItemID: "b1", Phone: "8613800000003", Status: "failed", Error: "x", SentAt: "2026-08-14T09:00:00+08:00"})
	e.recordMetric("udid-2", "task-b", "failed", false)
	// 旧格式：无任何归因信息 -> 未知设备组。
	e.finishItem(ItemResult{TaskID: "task-c", ItemID: "c1", Phone: "8613800000004", Status: "cancelled", SentAt: "2026-08-14T08:00:00+08:00"})

	groups, truncated := e.DeviceItems("", 0)
	if truncated {
		t.Fatal("unexpected truncation")
	}
	if len(groups) != 3 {
		t.Fatalf("want 3 groups, got %d: %+v", len(groups), groups)
	}
	g0 := groups[0]
	if g0.Udid != "udid-1" || g0.Serial != "SER1" || g0.Name != "运营机-01" || g0.SentOK != 2 || len(g0.Items) != 2 {
		t.Fatalf("group udid-1 wrong: %+v", g0)
	}
	if g0.Items[0].ItemID != "a2" || g0.Items[0].TaskID != "task-a" || g0.Items[1].ItemID != "a1" {
		t.Fatalf("group items not time-desc: %+v", g0.Items)
	}
	if groups[1].Udid != "udid-2" || groups[1].SentFail != 1 || groups[1].Serial != "SER2" {
		t.Fatalf("attributed group udid-2 wrong: %+v", groups[1])
	}
	if groups[2].Udid != "" || groups[2].Cancelled != 1 || len(groups[2].Items) != 1 {
		t.Fatalf("unknown group wrong: %+v", groups[2])
	}

	fg, _ := e.DeviceItems("udid-2", 0)
	if len(fg) != 1 || fg[0].Udid != "udid-2" || len(fg[0].Items) != 1 || fg[0].Items[0].ItemID != "b1" {
		t.Fatalf("udid filter wrong: %+v", fg)
	}
}

// TestEmptyPhoneRejected 明细缺手机号（含纯空白）直接标记失败、绝不进入 WDA 发送链路。
// 设备地址指向不可达端口：若守卫失效会得到连接类错误而非「缺少手机号」。
func TestEmptyPhoneRejected(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Devices: []Device{{UDID: "udid-e", IP: "127.0.0.1", Port: 1}}}
	e := NewExecutor(cfg, nil, nil, dir)
	e.Submit(TaskDispatch{
		TaskID: "task-e", UDID: "udid-e",
		Items: []TaskItem{{ItemID: "i1", Phone: ""}, {ItemID: "i2", Phone: "  "}},
	})

	var s TaskSummary
	deadline := time.After(5 * time.Second)
	for s.TaskID == "" {
		select {
		case s = <-e.SummaryQ:
		case <-deadline:
			t.Fatal("task did not finish")
		}
	}
	if s.SentFail != 2 || s.Status != taskDone {
		t.Fatalf("summary wrong: %+v", s)
	}
	items := e.readItems("task-e")
	for _, id := range []string{"i1", "i2"} {
		r, ok := items[id]
		if !ok {
			t.Fatalf("%s not persisted", id)
		}
		if r.Status != "failed" || !strings.Contains(r.Error, "缺少手机号") {
			t.Fatalf("%s should be rejected as failed: %+v", id, r)
		}
	}
	if m := e.Metrics("udid-e"); m.SentFail != 2 || m.Total != 2 {
		t.Fatalf("metrics wrong: %+v", m)
	}
}
