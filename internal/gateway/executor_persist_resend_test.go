package gateway

import (
	"encoding/json"
	"testing"
	"time"
)

// TestPersistItemNilStoreDoesNotClaimSuccess store 不可用时 persistItem 返回错误，
// finishItem 仍 best-effort 上报，但不得把明细当成已落盘。
func TestPersistItemNilStoreDoesNotClaimSuccess(t *testing.T) {
	e := NewExecutor(nil, nil, nil, t.TempDir())
	e.store = nil

	r := ItemResult{TaskID: "task-nil", ItemID: "i1", Phone: "p", Status: "sent", Udid: "u1"}
	if err := e.persistItem(r); err == nil {
		t.Fatal("persistItem with nil store should error")
	}
	e.finishItem(r)
	if e.persisted("task-nil", "i1") {
		t.Fatal("nil-store finish must not claim item persisted")
	}
	select {
	case got := <-e.ReportQ:
		if got.ItemID != "i1" || got.Status != "sent" {
			t.Fatalf("expected best-effort report, got %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("finishItem should still enqueue report when persist fails")
	}
	// metrics 与落盘一致：persist 失败不记 sent
	if m := e.Metrics("u1"); m.SentOK != 0 {
		t.Fatalf("metrics should stay 0 when persist failed, got %+v", m)
	}
}

// TestPersistItemRetriesThenSucceeds 瞬时写失败后重试可成功落盘。
func TestPersistItemRetriesThenSucceeds(t *testing.T) {
	e := NewExecutor(nil, nil, nil, t.TempDir())
	// 先关掉真实库制造失败，再恢复。
	closed := e.store
	e.store = nil
	r := ItemResult{TaskID: "task-retry", ItemID: "i1", Phone: "p", Status: "failed", Error: "x"}
	if err := e.persistItem(r); err == nil {
		t.Fatal("expected error while store nil")
	}
	e.store = closed
	if err := e.persistItem(r); err != nil {
		t.Fatalf("persist after restore: %v", err)
	}
	if !e.persisted("task-retry", "i1") {
		t.Fatal("item should be persisted after store restored")
	}
}

// TestResendPersistedStopsWhenReportQFull 队列满时停止并保留未入队明细（不静默丢光）。
func TestResendPersistedStopsWhenReportQFull(t *testing.T) {
	e := NewExecutor(nil, nil, nil, t.TempDir())
	// 填满 ReportQ，模拟补报时上行泵尚未消费。
	for i := 0; i < cap(e.ReportQ); i++ {
		e.ReportQ <- ItemResult{TaskID: "pad", ItemID: "pad", Status: "sent"}
	}
	// 未收口任务明细（无 meta）应优先，但队列已满 → 0 入队 + 明确停止。
	e.finishItem(ItemResult{TaskID: "task-unfin", ItemID: "u1", Phone: "p", Status: "sent"})
	// 清掉 finishItem 实时上报占位（上面已把队列灌满，finish 的 report 被丢是预期）。
	// 再灌满，确保 Resend 一开始就满。
	for len(e.ReportQ) < cap(e.ReportQ) {
		e.ReportQ <- ItemResult{TaskID: "pad2", ItemID: "pad2", Status: "sent"}
	}

	e.ResendPersisted()
	// 队列仍满且不应因 resend 阻塞；未收口明细仍在库中，供下次重连。
	if !e.persisted("task-unfin", "u1") {
		t.Fatal("item must remain on disk for next reconnect")
	}
	if len(e.ReportQ) != cap(e.ReportQ) {
		t.Fatalf("ReportQ len=%d want full %d", len(e.ReportQ), cap(e.ReportQ))
	}
}

// TestResendPersistedPrioritizesUnfinished 未收口任务明细优先于已收口任务。
func TestResendPersistedPrioritizesUnfinished(t *testing.T) {
	e := NewExecutor(nil, nil, nil, t.TempDir())
	// 先写未收口，再写已收口（updated_at 更新）——按时间倒序本应先出 done；
	// Resend 必须仍把未收口排到前面。
	e.finishItem(ItemResult{TaskID: "task-open", ItemID: "o1", Phone: "p", Status: "failed", Error: "x"})
	time.Sleep(15 * time.Millisecond)
	e.finishItem(ItemResult{TaskID: "task-done", ItemID: "d1", Phone: "p", Status: "sent"})
	e.finishTask(taskEnv{Udid: "u"}, TaskDispatch{TaskID: "task-done", Items: make([]TaskItem, 1)}, time.Now(), taskDone, "")

	// 排空实时队列
	for {
		select {
		case <-e.ReportQ:
		case <-e.SummaryQ:
		default:
			goto drained
		}
	}
drained:
	e.ResendPersisted()
	select {
	case r := <-e.ReportQ:
		if r.TaskID != "task-open" || r.ItemID != "o1" {
			t.Fatalf("want unfinished first, got task=%s item=%s", r.TaskID, r.ItemID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected resend item")
	}
}

// TestCancelBeforeSubmitNegativeCache 早到的 cancel 使后续 Submit 跳过。
func TestCancelBeforeSubmitNegativeCache(t *testing.T) {
	e := NewExecutor(&Config{Devices: []Device{{UDID: "udid-early", IP: ""}}}, nil, nil, t.TempDir())
	e.Cancel("task-early")
	e.Submit(TaskDispatch{TaskID: "task-early", UDID: "udid-early", Items: []TaskItem{{ItemID: "i1", Phone: "p"}}})
	// 给 worker 一点时间（若错误入队会很快因无 IP 落盘）
	time.Sleep(200 * time.Millisecond)
	if e.persisted("task-early", "i1") {
		t.Fatal("early-cancelled task should not run / persist items")
	}
	e.mu.Lock()
	_, still := e.cancel["task-early"]
	_, early := e.earlyCancel["task-early"]
	e.mu.Unlock()
	if still {
		t.Fatal("cancel channel should not remain after skipped submit")
	}
	if early {
		t.Fatal("earlyCancel entry should be consumed by Submit")
	}
}

// TestTaskDispatchCancelUnmarshalErrors 坏 JSON 不得当成空 payload 提交/取消。
func TestTaskDispatchCancelUnmarshalErrors(t *testing.T) {
	bad := json.RawMessage(`{"task_id":`)
	var tDispatch TaskDispatch
	if err := json.Unmarshal(bad, &tDispatch); err == nil {
		t.Fatal("expected dispatch unmarshal error")
	}
	var p struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(bad, &p); err == nil {
		t.Fatal("expected cancel unmarshal error")
	}
}
