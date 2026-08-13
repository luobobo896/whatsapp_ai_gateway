package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"wda-farm-gateway/internal/wda"
)

// TaskItem 群发单条明细。
type TaskItem struct {
	ItemID string `json:"item_id"`
	Phone  string `json:"phone"`
	Seq    int    `json:"seq"`
}

// TaskDispatch 平台下发的 task:dispatch。
type TaskDispatch struct {
	TaskID      string          `json:"task_id"`
	DeviceID    string          `json:"device_id"`
	UDID        string          `json:"udid"`
	Content     string          `json:"content"`
	IntervalSec int             `json:"interval_sec"`
	Schedule    GatewaySchedule `json:"schedule,omitempty"`
	Items       []TaskItem      `json:"items"`
}

// GatewaySchedule 群发智能节奏/熔断参数（与平台 BroadcastSchedule 对齐）。
type GatewaySchedule struct {
	IntervalJitterSec   int    `json:"intervalJitterSec"`
	BurstCount          int    `json:"burstCount"`
	BurstPauseSec       int    `json:"burstPauseSec"`
	WindowStart         string `json:"windowStart"`
	WindowEnd           string `json:"windowEnd"`
	MaxConsecutiveFails int    `json:"maxConsecutiveFails"`
}

// ItemResult 单条发送结果（item:result 上行）。
type ItemResult struct {
	TaskID     string `json:"task_id"`
	ItemID     string `json:"item_id"`
	Phone      string `json:"phone"`
	Status     string `json:"status"`
	Error      string `json:"error"`
	DurationMs int64  `json:"duration_ms"`
}

// DeviceStatus device:status 上行。
type DeviceStatus struct {
	UDID      string `json:"udid"`
	WDAStatus string `json:"wda_status"`
	Error     string `json:"error"`
}

// Executor 按 UDID 串行执行群发任务，先本地持久化结果再上报（at-least-once）。
type Executor struct {
	cfg        *Config
	wda        *WDAManager
	llm        *LLMClient
	resultsDir string

	mu      sync.Mutex
	queues  map[string]chan TaskDispatch
	workers map[string]bool
	cancel  map[string]chan struct{}
	busy    map[string]bool

	ReportQ chan ItemResult
	StatusQ chan DeviceStatus

	metricsMu sync.Mutex
	metrics   map[string]Metrics
}

// Metrics 网关本地发送统计。
type Metrics struct {
	SentOK   int     `json:"sent_ok"`
	SentFail int     `json:"sent_fail"`
	Total    int     `json:"total"`
	BatchID  string  `json:"batch_id"`
	LastTime float64 `json:"last_time"`
}

// NewExecutor 构造执行器。
func NewExecutor(cfg *Config, wdaMgr *WDAManager, llm *LLMClient, resultsDir string) *Executor {
	return &Executor{
		cfg: cfg, wda: wdaMgr, llm: llm, resultsDir: resultsDir,
		queues:  map[string]chan TaskDispatch{},
		workers: map[string]bool{},
		cancel:  map[string]chan struct{}{},
		busy:    map[string]bool{},
		ReportQ: make(chan ItemResult, 256),
		StatusQ: make(chan DeviceStatus, 256),
		metrics: map[string]Metrics{},
	}
}

// Submit 收到 task:dispatch：入队（同一 task 重复下发幂等）。
func (e *Executor) Submit(t TaskDispatch) {
	if t.TaskID == "" || t.UDID == "" {
		slog.Warn("dispatch missing task_id/udid", "payload", t)
		return
	}
	e.mu.Lock()
	if _, ok := e.cancel[t.TaskID]; ok {
		e.mu.Unlock()
		return
	}
	ch := e.queues[t.UDID]
	if ch == nil {
		ch = make(chan TaskDispatch, 16)
		e.queues[t.UDID] = ch
	}
	e.cancel[t.TaskID] = make(chan struct{})
	if !e.workers[t.UDID] {
		e.workers[t.UDID] = true
		go e.runUDID(t.UDID)
	}
	e.mu.Unlock()
	ch <- t
}

// Cancel 收到 task:cancel。
func (e *Executor) Cancel(taskID string) {
	e.mu.Lock()
	ch := e.cancel[taskID]
	e.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// IsBusy 某 UDID 是否执行中。
func (e *Executor) IsBusy(udid string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.busy[udid]
}

// Status 执行器状态。
func (e *Executor) Status() map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	queued := map[string]int{}
	for u, ch := range e.queues {
		queued[u] = len(ch)
	}
	busy := make([]string, 0, len(e.busy))
	for u := range e.busy {
		busy = append(busy, u)
	}
	sort.Strings(busy)
	return map[string]any{"busy_udids": busy, "queued": queued}
}

// Metrics 返回某 UDID 本地统计快照。
func (e *Executor) Metrics(udid string) Metrics {
	e.metricsMu.Lock()
	defer e.metricsMu.Unlock()
	return e.metrics[udid]
}

func (e *Executor) recordMetric(udid, taskID, status string) {
	e.metricsMu.Lock()
	defer e.metricsMu.Unlock()
	m := e.metrics[udid]
	if status == "sent" {
		m.SentOK++
	} else if status == "failed" {
		m.SentFail++
	}
	m.Total = m.SentOK + m.SentFail
	if taskID != "" {
		m.BatchID = taskID
	}
	m.LastTime = float64(time.Now().Unix())
	e.metrics[udid] = m
}

func (e *Executor) runUDID(udid string) {
	for {
		e.mu.Lock()
		ch := e.queues[udid]
		e.mu.Unlock()
		if ch == nil {
			return
		}
		t, ok := <-ch
		if !ok {
			return
		}
		e.processTask(udid, t)
		e.mu.Lock()
		empty := len(e.queues[udid]) == 0
		if empty {
			delete(e.workers, udid)
		}
		e.mu.Unlock()
		if empty {
			return
		}
	}
}

func (e *Executor) processTask(udid string, t TaskDispatch) {
	dev := e.cfg.Device(udid)
	ip := ""
	port := 8100
	if dev != nil {
		ip = dev.IP
		port = dev.Port
	}
	if ip == "" {
		for _, it := range t.Items {
			e.persist(t.TaskID, it.ItemID, it.Phone, "failed", "device ip not configured", 0)
			e.ReportQ <- ItemResult{TaskID: t.TaskID, ItemID: it.ItemID, Phone: it.Phone, Status: "failed", Error: "device ip not configured"}
		}
		return
	}
	e.mu.Lock()
	e.busy[udid] = true
	cancelCh := e.cancel[t.TaskID]
	e.mu.Unlock()
	e.StatusQ <- DeviceStatus{UDID: udid, WDAStatus: "busy"}
	defer func() {
		e.mu.Lock()
		delete(e.busy, udid)
		delete(e.cancel, t.TaskID)
		e.mu.Unlock()
		e.StatusQ <- DeviceStatus{UDID: udid, WDAStatus: "online"}
	}()

	sched := t.Schedule
	maxFails := sched.MaxConsecutiveFails
	if maxFails <= 0 {
		maxFails = 5
	}
	client := wda.NewClient(fmt.Sprintf("http://%s:%d", ip, port), 40*time.Second)
	consecFails := 0

	for idx, it := range t.Items {
		select {
		case <-cancelCh:
			e.markCancelled(t, it)
			continue
		default:
		}
		if e.persisted(t.TaskID, it.ItemID) {
			continue
		}
		// 发送时间窗：窗口外等待到窗口开始（可被取消中断）。
		if !withinWindow(sched.WindowStart, sched.WindowEnd) {
			if waitUntilWindow(cancelCh, sched.WindowStart) {
				e.markCancelled(t, it)
				continue
			}
		}
		t0 := time.Now()
		status, errMsg := "sent", ""
		assist := e.llm
		if assist != nil && assist.Model == "" {
			assist = nil
		}
		var serr error
		if assist == nil {
			serr = wda.SendMessageToPhone(context.Background(), client, it.Phone, t.Content)
		} else {
			serr = wda.SendMessageWithAssist(context.Background(), client, it.Phone, t.Content, assist)
		}
		if serr != nil {
			status, errMsg = "failed", serr.Error()
		}
		dur := time.Since(t0).Milliseconds()
		e.persist(t.TaskID, it.ItemID, it.Phone, status, errMsg, dur)
		e.ReportQ <- ItemResult{TaskID: t.TaskID, ItemID: it.ItemID, Phone: it.Phone, Status: status, Error: errMsg, DurationMs: dur}
		e.recordMetric(udid, t.TaskID, status)
		slog.Info("item done", "task", t.TaskID, "item", it.ItemID, "phone", it.Phone, "status", status, "duration_ms", dur)

		if status == "failed" {
			if containsAny(errMsg, "not reachable", "connection", "timed out") {
				slog.Warn("device unreachable, stop task", "task", t.TaskID, "udid", udid)
				return
			}
			consecFails++
			if consecFails >= maxFails {
				e.cancelRemaining(t, t.Items[idx+1:])
				e.StatusQ <- DeviceStatus{UDID: udid, WDAStatus: "online", Error: fmt.Sprintf("熔断：连续失败 %d 条", consecFails)}
				slog.Warn("circuit breaker triggered", "task", t.TaskID, "udid", udid, "consecutive_fails", consecFails)
				return
			}
		} else {
			consecFails = 0
		}

		// 拟人节奏：随机间隔 + 每 N 条长暂停。
		if wait := nextInterval(sched, t.IntervalSec); wait > 0 {
			if interrupted(cancelCh, wait) {
				continue
			}
		}
		if sched.BurstCount > 0 && sched.BurstPauseSec > 0 && (idx+1)%sched.BurstCount == 0 && idx+1 < len(t.Items) {
			if interrupted(cancelCh, time.Duration(sched.BurstPauseSec)*time.Second) {
				continue
			}
		}
	}
}

func (e *Executor) markCancelled(t TaskDispatch, it TaskItem) {
	e.persist(t.TaskID, it.ItemID, it.Phone, "cancelled", "cancelled by platform", 0)
	e.ReportQ <- ItemResult{TaskID: t.TaskID, ItemID: it.ItemID, Phone: it.Phone, Status: "cancelled", Error: "cancelled by platform"}
}

func (e *Executor) cancelRemaining(t TaskDispatch, rest []TaskItem) {
	for _, it := range rest {
		e.persist(t.TaskID, it.ItemID, it.Phone, "cancelled", "cancelled by circuit breaker", 0)
		e.ReportQ <- ItemResult{TaskID: t.TaskID, ItemID: it.ItemID, Phone: it.Phone, Status: "cancelled", Error: "cancelled by circuit breaker"}
	}
}

// interrupted 等待 d 时长，若期间被取消返回 true。
func interrupted(cancelCh <-chan struct{}, d time.Duration) bool {
	select {
	case <-cancelCh:
		return true
	case <-time.After(d):
		return false
	}
}

// nextInterval 根据节奏参数计算条间等待时长（随机抖动，下限 1s）。
func nextInterval(s GatewaySchedule, base int) time.Duration {
	if base <= 0 {
		base = 1
	}
	if s.IntervalJitterSec > 0 {
		base += rand.IntN(2*s.IntervalJitterSec+1) - s.IntervalJitterSec
		if base < 1 {
			base = 1
		}
	}
	return time.Duration(base) * time.Second
}

// withinWindow 判断当前本地时间是否在发送窗口内（HH:MM；空=不限制）。
func withinWindow(start, end string) bool {
	if start == "" && end == "" {
		return true
	}
	hm := time.Now().Hour()*60 + time.Now().Minute()
	if start != "" && end != "" {
		s, en := parseHM(start), parseHM(end)
		if s < en {
			return hm >= s && hm < en
		}
		return hm >= s || hm < en // 跨天窗口
	}
	if start != "" {
		return hm >= parseHM(start)
	}
	return hm < parseHM(end)
}

// waitUntilWindow 等待到下一个窗口开始；期间被取消返回 true。
func waitUntilWindow(cancelCh <-chan struct{}, start string) bool {
	if start == "" {
		return false
	}
	m := parseHM(start)
	now := time.Now()
	next := time.Date(now.Year(), now.Month(), now.Day(), m/60, m%60, 0, 0, now.Location())
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return interrupted(cancelCh, time.Until(next))
}

func parseHM(s string) int {
	var h, m int
	fmt.Sscanf(s, "%d:%d", &h, &m)
	return h*60 + m
}

// ---- 本地持久化（at-least-once）----

func (e *Executor) resultFile(taskID string) string {
	return filepath.Join(e.resultsDir, taskID+".json")
}

func (e *Executor) persisted(taskID, itemID string) bool {
	b, err := os.ReadFile(e.resultFile(taskID))
	if err != nil {
		return false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	_, ok := m[itemID]
	return ok
}

func (e *Executor) persist(taskID, itemID, phone, status, errMsg string, dur int64) {
	if err := os.MkdirAll(e.resultsDir, 0o755); err != nil {
		return
	}
	p := e.resultFile(taskID)
	m := map[string]map[string]any{}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	m[itemID] = map[string]any{"phone": phone, "status": status, "error": errMsg, "duration_ms": dur}
	b, _ := json.Marshal(m)
	tmp := p + ".tmp"
	_ = os.WriteFile(tmp, b, 0o600)
	_ = os.Rename(tmp, p)
}

// ResendPersisted 重连后补报本地已持久化结果。
func (e *Executor) ResendPersisted() {
	entries, err := os.ReadDir(e.resultsDir)
	if err != nil {
		return
	}
	for _, ent := range entries {
		if ent.IsDir() || filepath.Ext(ent.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(e.resultsDir, ent.Name()))
		if err != nil {
			continue
		}
		var m map[string]map[string]any
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		taskID := ent.Name()[:len(ent.Name())-5]
		for itemID, r := range m {
			e.ReportQ <- ItemResult{
				TaskID: taskID, ItemID: itemID,
				Phone:      str(r["phone"]),
				Status:     str(r["status"]),
				Error:      str(r["error"]),
				DurationMs: int64(num(r["duration_ms"])),
			}
		}
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && contains(s, sub) {
			return true
		}
	}
	return false
}
func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}
