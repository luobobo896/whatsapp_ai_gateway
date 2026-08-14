package gateway

import (
	"sync"
	"testing"
	"time"
)

// TestExecutorStressQueue 压测队列路径：空 IP 设备（快速失败路径）大量任务入队，
// 结果必须不丢不漏；并发 Submit/Cancel 无 panic（配合 -race 跑）。
func TestExecutorStressQueue(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Devices: []Device{{UDID: "udid-stress", IP: "", AutoReactivate: false}}}
	e := NewExecutor(cfg, nil, nil, dir)

	const tasks = 50
	const itemsPerTask = 10
	total := tasks * itemsPerTask

	// 先起结果回收协程：ReportQ 容量有限（256），不并发回收会与入队互相阻塞。
	resultsCh := make(chan struct{}, total)
	go func() {
		for range e.ReportQ {
			resultsCh <- struct{}{}
		}
	}()
	// SummaryQ 容量 64 > 任务数，收尾统一回收即可（收齐汇总也保证 meta 落盘已完成）。
	summaries := make(chan struct{}, tasks)
	go func() {
		for range e.SummaryQ {
			summaries <- struct{}{}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < tasks; i++ {
		items := make([]TaskItem, itemsPerTask)
		for j := range items {
			items[j] = TaskItem{ItemID: "item", Phone: "8613800000000", Seq: j + 1}
		}
		e.Submit(TaskDispatch{TaskID: genTaskID(t, i), UDID: "udid-stress", Items: items})
	}
	// 并发取消一半任务（幂等安全）。
	for i := 0; i < tasks; i += 2 {
		wg.Add(1)
		go func(n int) { defer wg.Done(); e.Cancel(genTaskID(t, n)) }(i)
	}
	wg.Wait()

	deadline := time.After(20 * time.Second)
	results := 0
	for results < total {
		select {
		case <-resultsCh:
			results++
		case <-deadline:
			t.Fatalf("timeout: got %d/%d item results", results, total)
		}
	}
	if results != total {
		t.Fatalf("results = %d, want %d", results, total)
	}
	// 收齐每个任务的 task:summary（含 meta 落盘收口），避免与 TempDir 清理竞争。
	gotSummaries := 0
	for gotSummaries < tasks {
		select {
		case <-summaries:
			gotSummaries++
		case <-deadline:
			t.Fatalf("timeout: got %d/%d task summaries", gotSummaries, tasks)
		}
	}
}

// TestExecutorStatusConcurrency 并发 Status/IsBusy/Metrics 快照无竞态。
func TestExecutorStatusConcurrency(t *testing.T) {
	e := NewExecutor(nil, nil, nil, t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = e.Status()
				_ = e.IsBusy("udid-x")
				_ = e.Metrics("udid-x")
				_ = e.MetricsSummary()
			}
		}()
	}
	wg.Wait()
}

func genTaskID(t *testing.T, n int) string {
	t.Helper()
	return "stress-task-" + string(rune('A'+n%26)) + "-" + itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
