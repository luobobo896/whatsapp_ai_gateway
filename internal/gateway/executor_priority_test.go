package gateway

import "testing"

// 本地任务优先：agent 队列有货时先取 agent，即使 cloud 也有货。
func TestUDIDQueuePrefersAgent(t *testing.T) {
	q := &udidQueue{agent: make(chan TaskDispatch, 2), cloud: make(chan TaskDispatch, 2)}
	q.agent <- TaskDispatch{TaskID: "auto-1", Source: "agent"}
	q.cloud <- TaskDispatch{TaskID: "cloud-1"}
	got := q.nextTask()
	if got.TaskID != "auto-1" || got.Source != "agent" {
		t.Fatalf("agent must be preferred, got %+v", got)
	}
}

// 路由：Source=agent 进 agent 队列，其余进 cloud 队列。
func TestQChanForRoutesBySource(t *testing.T) {
	q := &udidQueue{agent: make(chan TaskDispatch, 2), cloud: make(chan TaskDispatch, 2)}
	if qChanFor(q, "agent") != q.agent {
		t.Fatal("agent source must route to agent queue")
	}
	if qChanFor(q, "") != q.cloud || qChanFor(q, "cloud") != q.cloud {
		t.Fatal("non-agent source must route to cloud queue")
	}
}

// TaskList 的 Running 标志：未收口（在 cancel map）即视为执行中。
func TestIsRunningTaskFlag(t *testing.T) {
	e := NewExecutor(nil, nil, nil, t.TempDir())
	e.mu.Lock()
	e.cancel["t1"] = make(chan struct{})
	e.mu.Unlock()
	if !e.isRunning("t1") {
		t.Fatal("task in cancel map should be running")
	}
	e.mu.Lock()
	delete(e.cancel, "t1")
	e.mu.Unlock()
	if e.isRunning("t1") {
		t.Fatal("task removed should not be running")
	}
}
