package gateway

import (
	"context"
	"strings"
	"testing"
	"time"
)

const dummyLLMURL = "http://127.0.0.1:9"

func fixedNow() time.Time { return time.Date(2026, 8, 26, 10, 0, 0, 0, time.Local) }

// testAutonomy 构造带一台在线设备（u1）与一台离线设备（u2）的自主回路。
func testAutonomy(t *testing.T, mk func(*Config)) *AutonomyLoop {
	t.Helper()
	cfg := &Config{Devices: []Device{
		{UDID: "u1", IP: "127.0.0.1", Port: 1, LastHealth: map[string]any{"ok": true}},
		{UDID: "u2", IP: "127.0.0.1", Port: 2, LastHealth: map[string]any{"ok": false}},
	}}
	if mk != nil {
		mk(cfg)
	}
	ex := NewExecutor(cfg, nil, nil, t.TempDir())
	gw := New(cfg, nil, ex, nil, nil)
	a := NewAutonomyLoop(gw)
	a.now = fixedNow
	return a
}

// step 在任何“没有群发”的分支都应静默返回（不调模型、不写决策事件）。
func TestAutonomyStepSilentWhenDisabled(t *testing.T) {
	a := testAutonomy(t, func(c *Config) { c.Autonomy = AutonomyConfig{Enabled: false, Content: "hi"} })
	a.llm = NewLLMClient(dummyLLMURL, "k", "m")
	if err := a.step(context.Background()); err != nil {
		t.Fatalf("step error: %v", err)
	}
}

func TestAutonomyStepSilentWhenNoDevice(t *testing.T) {
	a := testAutonomy(t, func(c *Config) {
		c.Autonomy = AutonomyConfig{Enabled: true, Content: "hi"}
		c.Devices = nil
	})
	a.llm = NewLLMClient(dummyLLMURL, "k", "m")
	if err := a.step(context.Background()); err != nil {
		t.Fatalf("step error: %v", err)
	}
}

func TestAutonomyStepSilentOutsideWindow(t *testing.T) {
	a := testAutonomy(t, func(c *Config) {
		c.Autonomy = AutonomyConfig{Enabled: true, Content: "hi", WindowStart: "23:00", WindowEnd: "23:59"}
	})
	a.llm = NewLLMClient(dummyLLMURL, "k", "m")
	if err := a.step(context.Background()); err != nil {
		t.Fatalf("step error: %v", err)
	}
}

func TestAutonomyStepSilentWhenBudgetReached(t *testing.T) {
	a := testAutonomy(t, func(c *Config) {
		c.Autonomy = AutonomyConfig{Enabled: true, Content: "hi", DailyCap: 1}
	})
	a.llm = NewLLMClient(dummyLLMURL, "k", "m")
	a.g.Exec.recordMetric("u1", "t", "sent", false) // 今日 sent=1 -> remain=0
	if err := a.step(context.Background()); err != nil {
		t.Fatalf("step error: %v", err)
	}
}

func TestAutonomyStepSilentWhenAlreadySubmitted(t *testing.T) {
	a := testAutonomy(t, func(c *Config) {
		c.Autonomy = AutonomyConfig{Enabled: true, Content: "hi", DailyCap: 40}
	})
	a.llm = NewLLMClient(dummyLLMURL, "k", "m")
	a.st.markSubmitted("u1", "auto-x", a.now())
	if err := a.step(context.Background()); err != nil {
		t.Fatalf("step error: %v", err)
	}
}

// 守卫：模型输出不可信，越权/越预算/坏 intent/无目标一律拒绝。
func TestAutonomyGuard(t *testing.T) {
	a := &AutonomyLoop{}
	plan := AutonomyConfig{Content: "hi"}
	targets := []agentCandidate{{UDID: "u1", Remain: 40}}

	sub, ok := a.guard(plan, map[string]any{"intent": "submit", "udid": "u1"}, targets)
	if !ok || sub.UDID != "u1" || sub.Content != "hi" {
		t.Fatalf("valid decision rejected: ok=%v sub=%+v", ok, sub)
	}
	if _, ok := a.guard(plan, map[string]any{"intent": "idle", "udid": "u1"}, targets); ok {
		t.Fatal("intent=idle must not submit")
	}
	if _, ok := a.guard(plan, map[string]any{"intent": "submit", "udid": "u9"}, targets); ok {
		t.Fatal("unknown udid must be rejected")
	}
	if _, ok := a.guard(AutonomyConfig{}, map[string]any{"intent": "submit", "udid": "u1"}, targets); ok {
		t.Fatal("empty content must be rejected")
	}
	if _, ok := a.guard(plan, map[string]any{"intent": "submit", "udid": "u1"}, nil); ok {
		t.Fatal("empty targets must be rejected")
	}
}

// 预筛：当日已提交的设备被排除，只剩未发的 u2。
func TestAutonomyPrefilter(t *testing.T) {
	a := &AutonomyLoop{now: fixedNow}
	a.st.day = "2026-08-26"
	a.st.submitted = map[string]string{"u1": "auto-x"}
	plan := AutonomyConfig{DailyCap: 40}
	st := agentStateView{Devices: []agentDeviceView{
		{UDID: "u1", Remain: 40, Healthy: true},
		{UDID: "u2", Remain: 40, Healthy: true},
	}}
	got := a.prefilter(plan, st)
	if len(got) != 1 || got[0].UDID != "u2" {
		t.Fatalf("want only u2, got %+v", got)
	}
}

// submit 落到 Executor：本地优先（agent 队列），任务 id 以 auto- 开头。
func TestAutonomySubmitDispatchesAgentTask(t *testing.T) {
	a := testAutonomy(t, func(c *Config) {
		c.Autonomy = AutonomyConfig{Enabled: true, Content: "hi", DailyCap: 40}
	})
	ex := a.g.Exec
	plan := a.normalizePlan(a.cfg().Autonomy)
	taskID := a.submit(plan, agentSubmission{UDID: "u1", Content: plan.Content})
	if !strings.HasPrefix(taskID, "auto-") {
		t.Fatalf("task id should be auto-*, got %q", taskID)
	}
	select {
	case s := <-ex.SummaryQ:
		if s.TaskID != taskID || s.Udid != "u1" {
			t.Fatalf("summary task mismatch: %+v", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("agent task not dispatched to executor")
	}
}

// 幂等键：同一天同话术同设备 -> 同 task_id；跨天不同。
func TestAutoTaskID(t *testing.T) {
	at := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	id1 := autoTaskID("u1", "hi", at)
	id2 := autoTaskID("u1", "hi", at.Add(3*time.Hour))
	id3 := autoTaskID("u1", "hi", at.AddDate(0, 0, 1))
	if id1 != id2 {
		t.Fatalf("same day should be idempotent: %q vs %q", id1, id2)
	}
	if id1 == id3 {
		t.Fatalf("different day should differ: %q == %q", id1, id3)
	}
	if !strings.HasPrefix(id1, "auto-") || len(id1) != 25 {
		t.Fatalf("bad task id %q", id1)
	}
}

func TestWithinWindowAt(t *testing.T) {
	loc := time.Local
	if !withinWindowAt("", "", loc, fixedNow()) {
		t.Fatal("empty window should always be in")
	}
	if withinWindowAt("23:00", "23:59", loc, fixedNow()) {
		t.Fatal("10:00 should be outside 23:00-23:59")
	}
	if !withinWindowAt("09:00", "12:00", loc, fixedNow()) {
		t.Fatal("10:00 should be inside 09:00-12:00")
	}
}

// 预算硬约束：单批 = min(MaxFriends, 今日剩余预算)，最少 1，绝不超发。
func TestAutonomyBatchHardCap(t *testing.T) {
	cases := []struct{ daily, friend, sent, want int }{
		{40, 30, 0, 30},
		{40, 30, 30, 10},
		{40, 30, 39, 1},
		{40, 100, 10, 30},
	}
	for _, c := range cases {
		if got := autonomyBatch(c.daily, c.friend, c.sent); got != c.want {
			t.Fatalf("autonomyBatch(%d,%d,%d)=%d want %d", c.daily, c.friend, c.sent, got, c.want)
		}
	}
}

// 模型不可用时确定性兜底：选剩余预算最大的一台。
func TestAutonomyFallbackPicksLargestRemain(t *testing.T) {
	a := &AutonomyLoop{}
	plan := AutonomyConfig{Content: "hi"}
	sub, ok := a.fallback(plan, []agentCandidate{{UDID: "u1", Remain: 5}, {UDID: "u2", Remain: 30}})
	if !ok || sub.UDID != "u2" || sub.Content != "hi" {
		t.Fatalf("fallback should pick u2: ok=%v sub=%+v", ok, sub)
	}
	if _, ok := a.fallback(plan, nil); ok {
		t.Fatal("empty targets should not fallback")
	}
}

// “为何未发”诊断状态可读。
func TestAutonomyStatusReport(t *testing.T) {
	a := testAutonomy(t, func(c *Config) { c.Autonomy = AutonomyConfig{Enabled: true, Content: "hi", DailyCap: 40} })
	a.setDiag("outside_window", "发送窗口外")
	st := a.Status()
	if !st.Enabled || st.DailyCap != 40 || st.Reason != "发送窗口外" || st.State != "outside_window" || st.LLMEnabled || st.Running {
		t.Fatalf("bad status: %+v", st)
	}
}

// 结果回填：最近一次自主任务把执行器真实结果（已发 N / 无新联系人）带到状态。
func TestAutonomyLastTaskResult(t *testing.T) {
	a := testAutonomy(t, func(c *Config) { c.Autonomy = AutonomyConfig{Enabled: true, Content: "hi"} })
	ex := a.g.Exec
	taskID := autoTaskID("u1", "hi", a.now())

	ex.store.putItem(taskID, "chat-list", itemRecord{Status: "sent", ChatListSent: 3}, time.Now())
	a.setLastTask("u1", taskID)
	tid, desc, ok := a.lastTaskResult()
	if !ok || tid != taskID || !strings.Contains(desc, "3") {
		t.Fatalf("sent result not backfilled: ok=%v tid=%q desc=%q", ok, tid, desc)
	}

	ex.store.putItem(taskID, "chat-list", itemRecord{Status: "failed", Error: "聊天列表未找到新的未触达联系人"}, time.Now())
	a.setLastTask("u1", taskID)
	_, desc, ok = a.lastTaskResult()
	if !ok || !strings.Contains(desc, "没有新的未触达") {
		t.Fatalf("no-new-contact result not backfilled: ok=%v desc=%q", ok, desc)
	}
}
