package gateway

import (
	"os"
	"testing"
	"time"
)

func mustExec(t *testing.T, dir string) *Executor {
	t.Helper()
	return NewExecutor(nil, nil, nil, dir)
}

func dayAt(t *testing.T, s string) func() time.Time {
	t.Helper()
	ts, err := time.ParseInLocation("2006-01-02 15:04:05", s+" 10:00:00", time.Local)
	if err != nil {
		t.Fatalf("parse day: %v", err)
	}
	return func() time.Time { return ts }
}

// TestMetricsPersistAcrossRestart 统计落盘：重启后（新 Executor）计数不丢。
func TestMetricsPersistAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	e := mustExec(t, dir)
	e.now = dayAt(t, "2026-08-13")
	e.recordMetric("udid-a", "task-1", "sent", false)
	e.recordMetric("udid-a", "task-1", "sent", false)
	e.recordMetric("udid-b", "task-1", "failed", false)

	// 模拟重启：同一目录新建 Executor。
	e2 := mustExec(t, dir)
	e2.now = dayAt(t, "2026-08-13")
	s := e2.MetricsSummary()
	if s.Today.SentOK != 2 || s.Today.SentFail != 1 || s.Today.Total != 3 {
		t.Fatalf("summary after restart = %+v", s)
	}
	if s.Devices["udid-a"].SentOK != 2 || s.Devices["udid-b"].SentFail != 1 {
		t.Fatalf("devices after restart = %+v", s.Devices)
	}
}

// TestMetricsDayRolloverFoldsHistory 跨天：昨日分设备计数折入 history，今日重新计数。
func TestMetricsDayRolloverFoldsHistory(t *testing.T) {
	dir := t.TempDir()
	e := mustExec(t, dir)
	e.now = dayAt(t, "2026-08-13")
	e.recordMetric("udid-a", "task-1", "sent", false)
	e.recordMetric("udid-a", "task-1", "failed", false)

	// 新的一天：昨日计数归档。
	e.now = dayAt(t, "2026-08-14")
	e.recordMetric("udid-b", "task-2", "sent", false)

	s := e.MetricsSummary()
	if s.Day != "2026-08-14" {
		t.Fatalf("summary day = %q", s.Day)
	}
	if s.Today.SentOK != 1 || s.Today.Total != 1 {
		t.Fatalf("today = %+v, want only new-day counts", s.Today)
	}
	if len(s.History) != 1 || s.History[0].Day != "2026-08-13" ||
		s.History[0].SentOK != 1 || s.History[0].SentFail != 1 {
		t.Fatalf("history = %+v", s.History)
	}
	if s.Devices["udid-a"].Total != 0 {
		t.Fatalf("devices should reset on rollover: %+v", s.Devices)
	}
}

// TestMetricsRolloverPersistsAcrossRestart 跨天归档后重启，history 与今日计数均保留。
func TestMetricsRolloverPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	e := mustExec(t, dir)
	e.now = dayAt(t, "2026-08-13")
	e.recordMetric("udid-a", "t1", "sent", false)
	e.now = dayAt(t, "2026-08-14")
	e.recordMetric("udid-a", "t2", "sent", false)

	e2 := mustExec(t, dir)
	e2.now = dayAt(t, "2026-08-14")
	s := e2.MetricsSummary()
	if s.Today.SentOK != 1 || len(s.History) != 1 || s.History[0].SentOK != 1 {
		t.Fatalf("persisted rollover summary = %+v", s)
	}
}

// TestMetricsCorruptFileIgnored 损坏的 metrics.json 不阻塞启动。
func TestMetricsCorruptFileIgnored(t *testing.T) {
	dir := t.TempDir()
	NewExecutor(nil, nil, nil, dir) // 建目录
	p := dir + "/metrics.json"
	if err := os.WriteFile(p, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := mustExec(t, dir)
	e.now = dayAt(t, "2026-08-13")
	e.recordMetric("udid-a", "t1", "sent", false)
	if got := e.MetricsSummary().Today.SentOK; got != 1 {
		t.Fatalf("sent after corrupt file = %d, want 1", got)
	}
}
