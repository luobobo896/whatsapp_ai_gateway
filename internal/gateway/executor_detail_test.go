package gateway

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// enrichedResult 构造一条带全部增强字段的明细（各测试共用）。
func enrichedResult(taskID, itemID string) ItemResult {
	return ItemResult{
		TaskID: taskID, ItemID: itemID, Phone: "8613800000001", Status: "sent", DurationMs: 1234,
		Udid: "udid-1", Serial: "SERIAL01", DeviceName: "测试机", ConnType: "usb",
		Content: "13800000001 您好", ContactName: "张三", SentAt: "2026-08-14T10:00:00+08:00", NewSession: true,
	}
}

// TestPersistItemEnriched 明细落盘含增强字段，且旧格式文件可继续读取（向后兼容）。
func TestPersistItemEnriched(t *testing.T) {
	e := NewExecutor(nil, nil, nil, t.TempDir())
	e.finishItem(enrichedResult("task-a", "item-1"))

	m := e.readItems("task-a")
	r, ok := m["item-1"]
	if !ok {
		t.Fatalf("item-1 not persisted: %+v", m)
	}
	if r.Udid != "udid-1" || r.ConnType != "usb" || r.Content != "13800000001 您好" ||
		r.ContactName != "张三" || r.SentAt == "" || !r.NewSession {
		t.Fatalf("enriched fields lost: %+v", r)
	}
}

// TestPersistItemLegacyFile 旧格式（map[string]any 落盘）文件能被读取并不破坏新写入。
func TestPersistItemLegacyFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "task-old.json")
	legacy := `{"item-1":{"phone":"8613800000001","status":"sent","error":"","duration_ms":900}}`
	if err := os.WriteFile(p, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	e := NewExecutor(nil, nil, nil, dir)
	e.finishItem(enrichedResult("task-old", "item-2"))

	m := e.readItems("task-old")
	if len(m) != 2 {
		t.Fatalf("want 2 items, got %d (%+v)", len(m), m)
	}
	if m["item-1"].Phone != "8613800000001" || m["item-1"].Status != "sent" {
		t.Fatalf("legacy item corrupted: %+v", m["item-1"])
	}
	if m["item-2"].Content != "13800000001 您好" {
		t.Fatalf("new item fields lost: %+v", m["item-2"])
	}
}

// TestFinishTaskSummaryCounts 汇总按落盘明细统计并写 meta；设备未配 IP 时 status=no_ip。
func TestFinishTaskSummaryCounts(t *testing.T) {
	e := NewExecutor(nil, nil, nil, t.TempDir())
	e.finishItem(enrichedResult("task-b", "item-1")) // sent
	e.finishItem(enrichedResult("task-b", "item-2")) // sent（改 failed）
	e.finishItem(ItemResult{TaskID: "task-b", ItemID: "item-3", Phone: "p", Status: "failed", Error: "x"})

	// 修正 item-2 为 failed（finishItem 覆盖写）。
	e.finishItem(ItemResult{TaskID: "task-b", ItemID: "item-2", Phone: "p", Status: "failed", Error: "y"})
	e.finishItem(ItemResult{TaskID: "task-b", ItemID: "item-4", Phone: "p", Status: "cancelled", Error: "r"})

	start := time.Now()
	e.finishTask(taskEnv{Udid: "udid-1", ConnType: "usb"}, TaskDispatch{TaskID: "task-b", Items: make([]TaskItem, 6)}, start, taskStopNoIP, "device ip not configured")

	s := e.readSummary("task-b")
	if s == nil {
		t.Fatal("summary meta not persisted")
	}
	if s.Status != taskStopNoIP || s.Total != 6 || s.SentOK != 1 || s.SentFail != 2 || s.Cancelled != 1 || s.Pending != 2 {
		t.Fatalf("summary counts wrong: %+v", s)
	}
	if s.Udid != "udid-1" || s.ConnType != "usb" || s.StartAt == "" || s.EndAt == "" {
		t.Fatalf("summary env fields missing: %+v", s)
	}
}

// TestFinishTaskSummarySync 上行队列收到 task:summary。
func TestFinishTaskSummarySync(t *testing.T) {
	e := NewExecutor(nil, nil, nil, t.TempDir())
	e.finishTask(taskEnv{Udid: "u"}, TaskDispatch{TaskID: "task-c"}, time.Now(), taskDone, "")
	select {
	case s := <-e.SummaryQ:
		if s.TaskID != "task-c" || s.Status != taskDone {
			t.Fatalf("unexpected summary: %+v", s)
		}
	default:
		t.Fatal("summary not enqueued")
	}
}

// TestTaskListDetailFiltering 任务列表排除 metrics.json 与 .meta.json；明细分页正确。
func TestTaskListDetailFiltering(t *testing.T) {
	dir := t.TempDir()
	// 干扰文件：统计文件与汇总文件都不应出现在任务列表。
	_ = os.WriteFile(filepath.Join(dir, "metrics.json"), []byte(`{"day":"2026-08-14"}`), 0o600)

	e := NewExecutor(nil, nil, nil, dir)
	for i := 0; i < 3; i++ {
		e.finishItem(enrichedResult("task-d", string(rune('a'+i))))
	}
	e.finishTask(taskEnv{Udid: "u", ConnType: "usb"}, TaskDispatch{TaskID: "task-d", Items: make([]TaskItem, 3)}, time.Now(), taskDone, "")

	list := e.TaskList()
	if len(list) != 1 || list[0].TaskID != "task-d" {
		t.Fatalf("task list should contain only task-d: %+v", list)
	}
	it := list[0]
	if !it.Finished || it.Items != 3 || it.SentOK != 3 || it.Summary == nil || it.Summary.Status != taskDone {
		t.Fatalf("task list item wrong: %+v", it)
	}

	items, total := e.TaskDetail("task-d", 1, 1)
	if total != 3 || len(items) != 1 || items[0].ItemID != "b" {
		t.Fatalf("detail pagination wrong: total=%d items=%+v", total, items)
	}
	if items[0].Content != "13800000001 您好" || items[0].ConnType != "usb" {
		t.Fatalf("detail enriched fields missing: %+v", items[0])
	}
}

// TestResendPersistedEnriched 补报带增强字段明细与任务汇总。
func TestResendPersistedEnriched(t *testing.T) {
	e := NewExecutor(nil, nil, nil, t.TempDir())
	e.finishItem(enrichedResult("task-e", "item-1"))
	e.finishItem(ItemResult{TaskID: "task-e", ItemID: "item-2", Phone: "p2", Status: "failed", Error: "boom"})
	e.finishTask(taskEnv{Udid: "udid-1", ConnType: "wifi"}, TaskDispatch{TaskID: "task-e", Items: make([]TaskItem, 2)}, time.Now(), taskDone, "")

	// 清掉实时上报队列，只验证补报内容。
	for {
		select {
		case <-e.ReportQ:
			continue
		case <-e.SummaryQ:
			continue
		default:
		}
		break
	}
	e.ResendPersisted()

	gotItems := 0
	gotSummary := false
loop:
	for {
		select {
		case r := <-e.ReportQ:
			gotItems++
			if r.ItemID == "item-1" && (r.Content != "13800000001 您好" || r.ContactName != "张三" || r.ConnType != "usb" || r.Udid != "udid-1") {
				t.Fatalf("resend lost enriched fields: %+v", r)
			}
		case s := <-e.SummaryQ:
			if s.TaskID == "task-e" {
				gotSummary = true
			}
		default:
			break loop
		}
	}
	if gotItems != 2 {
		t.Fatalf("resend items = %d, want 2", gotItems)
	}
	if !gotSummary {
		t.Fatal("resend summary missing")
	}
}

func TestChatListOutcomeRemembersCount(t *testing.T) {
	st, errMsg, contact := chatListOutcome(2, []string{"+86 152 1347 2085", "+86 176 8854 0775"}, nil)
	if st != "sent" || errMsg != "聊天列表已发送 2 人" {
		t.Fatalf("status=%s err=%q", st, errMsg)
	}
	if contact != "2人：+86 152 1347 2085、+86 176 8854 0775" {
		t.Fatalf("contact=%q", contact)
	}
	st, _, contact = chatListOutcome(0, nil, nil)
	if st != "failed" || contact != "" {
		t.Fatalf("empty: %s %q", st, contact)
	}
}
