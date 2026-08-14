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
	"strconv"
	"strings"
	"sync"
	"time"

	"wda-farm-gateway/internal/wda"
)

// TaskItem 群发单条明细。
type TaskItem struct {
	ItemID string `json:"item_id"`
	Phone  string `json:"phone"`
	Seq    int    `json:"seq"`
	// Content 是该条的最终渲染内容（平台模板变量逐条渲染下发）；空则沿用任务级 Content。
	Content string `json:"content,omitempty"`
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
	// 账号预热（设备级按天计数）：第 1 天 startCap，每日 +dailyStep，稳态 steadyCap。
	WarmUpEnabled   bool `json:"warmUpEnabled"`
	WarmUpStartCap  int  `json:"warmUpStartCap"`
	WarmUpDailyStep int  `json:"warmUpDailyStep"`
	WarmUpSteadyCap int  `json:"warmUpSteadyCap"`
	// MaxNewSessionRatio 当日新会话占比上限（百分比；0=不限制）。
	MaxNewSessionRatio int `json:"maxNewSessionRatio"`
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
	llmMu      sync.RWMutex
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
	// 落盘聚合（重启不丢统计，跨天自动归档到 history）：
	metricsDay     string
	metricsHistory map[string]Metrics
	now            func() time.Time // 测试可注入
}

// Metrics 网关本地发送统计。
type Metrics struct {
	SentOK   int `json:"sent_ok"`
	SentFail int `json:"sent_fail"`
	Total    int `json:"total"`
	// NewSessions 当日经「新聊天→搜索」打开的新会话数（新会话占比控制用）。
	NewSessions int     `json:"new_sessions"`
	BatchID     string  `json:"batch_id"`
	LastTime    float64 `json:"last_time"`
}

// metricsFileState 是 metrics.json 的落盘结构：当天分设备计数 + 历史按天聚合。
type metricsFileState struct {
	Day     string             `json:"day"`
	Devices map[string]Metrics `json:"devices"`
	History map[string]Metrics `json:"history"`
}

// MetricsSummary 是 /api/metrics 的聚合视图（网关级：今日汇总 + 分设备 + 历史按天）。
type MetricsSummary struct {
	Day     string             `json:"day"`
	Today   Metrics            `json:"today"`
	Devices map[string]Metrics `json:"devices"`
	History []MetricsHistory   `json:"history"`
}

// MetricsHistory 历史某天的聚合发送统计。
type MetricsHistory struct {
	Day      string `json:"day"`
	SentOK   int    `json:"sent_ok"`
	SentFail int    `json:"sent_fail"`
	Total    int    `json:"total"`
}

// NewExecutor 构造执行器并加载历史统计（metrics.json，重启不丢）。
func NewExecutor(cfg *Config, wdaMgr *WDAManager, llm *LLMClient, resultsDir string) *Executor {
	e := &Executor{
		cfg: cfg, wda: wdaMgr, llm: llm, resultsDir: resultsDir,
		queues:         map[string]chan TaskDispatch{},
		workers:        map[string]bool{},
		cancel:         map[string]chan struct{}{},
		busy:           map[string]bool{},
		ReportQ:        make(chan ItemResult, 256),
		StatusQ:        make(chan DeviceStatus, 256),
		metrics:        map[string]Metrics{},
		metricsHistory: map[string]Metrics{},
		now:            time.Now,
	}
	e.loadMetrics()
	return e
}

// ---- 统计落盘聚合 ----

// SetLLM 热替换运行时视觉/LLM 客户端（平台下发 model:config 后调用）。
func (e *Executor) SetLLM(llm *LLMClient) {
	e.llmMu.Lock()
	defer e.llmMu.Unlock()
	e.llm = llm
}

func (e *Executor) llmClient() *LLMClient {
	e.llmMu.RLock()
	defer e.llmMu.RUnlock()
	return e.llm
}

func (e *Executor) metricsFilePath() string {
	return filepath.Join(e.resultsDir, "metrics.json")
}

// loadMetrics 读入历史统计。落盘数据标注的日期保持原样（跨天归档由下次
// recordMetric 惰性完成），避免启动时刻与数据日期不一致时误归档。
func (e *Executor) loadMetrics() {
	b, err := os.ReadFile(e.metricsFilePath())
	if err != nil {
		return
	}
	var f metricsFileState
	if json.Unmarshal(b, &f) != nil {
		slog.Warn("metrics file corrupted, ignoring", "path", e.metricsFilePath())
		return
	}
	if f.History != nil {
		e.metricsHistory = f.History
	}
	if f.Devices != nil {
		e.metrics = f.Devices
	}
	if f.Day == "" {
		f.Day = e.today() // 旧格式/首写：当前数据视为今天
	}
	e.metricsDay = f.Day
}

func (e *Executor) today() string {
	return e.now().Format("2006-01-02")
}

// foldDayLocked 把当天分设备计数折入 history（需已持有 metricsMu）。
func (e *Executor) foldDayLocked(day string) {
	if day == "" {
		return
	}
	agg := e.metricsHistory[day]
	for _, m := range e.metrics {
		agg.SentOK += m.SentOK
		agg.SentFail += m.SentFail
		agg.Total += m.Total
		agg.NewSessions += m.NewSessions
		if m.LastTime > agg.LastTime {
			agg.LastTime = m.LastTime
		}
	}
	e.metricsHistory[day] = agg
	e.metrics = map[string]Metrics{}
}

// persistMetricsLocked 原子写盘（tmp+rename），失败仅告警不影响发送。
func (e *Executor) persistMetricsLocked() {
	if err := os.MkdirAll(e.resultsDir, 0o755); err != nil {
		return
	}
	f := metricsFileState{Day: e.metricsDay, Devices: e.metrics, History: e.metricsHistory}
	b, err := json.Marshal(f)
	if err != nil {
		return
	}
	p := e.metricsFilePath()
	tmp := p + ".tmp"
	if os.WriteFile(tmp, b, 0o600) != nil || os.Rename(tmp, p) != nil {
		slog.Warn("metrics persist failed", "path", p)
	}
}

// MetricsSummary 返回网关级聚合视图：今日汇总 + 分设备 + 历史按天（倒序）。
func (e *Executor) MetricsSummary() MetricsSummary {
	e.metricsMu.Lock()
	defer e.metricsMu.Unlock()
	today := Metrics{}
	devices := map[string]Metrics{}
	for udid, m := range e.metrics {
		today.SentOK += m.SentOK
		today.SentFail += m.SentFail
		today.Total += m.Total
		today.NewSessions += m.NewSessions
		if m.LastTime > today.LastTime {
			today.LastTime = m.LastTime
		}
		devices[udid] = m
	}
	history := make([]MetricsHistory, 0, len(e.metricsHistory))
	for day, m := range e.metricsHistory {
		history = append(history, MetricsHistory{Day: day, SentOK: m.SentOK, SentFail: m.SentFail, Total: m.Total})
	}
	sort.Slice(history, func(i, j int) bool { return history[i].Day > history[j].Day })
	return MetricsSummary{Day: e.metricsDay, Today: today, Devices: devices, History: history}
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

// todayMetrics 返回某 UDID 今日统计快照（预热/占比控制用）。
func (e *Executor) todayMetrics(udid string) Metrics {
	e.metricsMu.Lock()
	defer e.metricsMu.Unlock()
	return e.metrics[udid]
}

// daysActive 返回该设备活跃天数（历史归档天数 + 今天；用于预热放量阶段计算）。
func (e *Executor) daysActive() int {
	e.metricsMu.Lock()
	defer e.metricsMu.Unlock()
	return len(e.metricsHistory) + 1
}

// warmupDailyCap 预热第 days 天的日发送上限（第 1 天 startCap，每日 +step，稳态封顶）。
func warmupDailyCap(s GatewaySchedule, days int) int {
	if days < 1 {
		days = 1
	}
	start, step, steady := s.WarmUpStartCap, s.WarmUpDailyStep, s.WarmUpSteadyCap
	if start <= 0 {
		start = 5
	}
	if step <= 0 {
		step = 10
	}
	if steady <= 0 {
		steady = 40
	}
	cap := start + (days-1)*step
	if cap > steady {
		cap = steady
	}
	return cap
}

// newSessionRatioExceeded 判断「再开一个新会话」后是否超过占比上限（百分比整数比较，避免浮点）。
func newSessionRatioExceeded(todayNew, todayTotal, ratioPct int) bool {
	if ratioPct <= 0 {
		return false
	}
	if todayTotal <= 0 {
		return false // 首条不计入占比（分母为 0 时允许）
	}
	return (todayNew+1)*100 > ratioPct*(todayTotal+1)
}

// report 非阻塞上报单条结果：结果已先落盘，队列满时丢弃（重连后补报），避免断线时阻塞执行器。
func (e *Executor) report(r ItemResult) {
	select {
	case e.ReportQ <- r:
	default:
		slog.Warn("report queue full, dropped (persisted, will re-report)", "task", r.TaskID, "item", r.ItemID)
	}
}

// status 非阻塞上报设备状态（best-effort，队列满时丢弃）。
func (e *Executor) status(s DeviceStatus) {
	select {
	case e.StatusQ <- s:
	default:
	}
}

func (e *Executor) recordMetric(udid, taskID, status string, newSession bool) {
	e.metricsMu.Lock()
	defer e.metricsMu.Unlock()
	// 跨天：先把昨天分设备计数折入 history，再重置当天计数。
	today := e.today()
	if e.metricsDay != "" && e.metricsDay != today {
		e.foldDayLocked(e.metricsDay)
	}
	if e.metricsDay != today {
		e.metricsDay = today
	}
	m := e.metrics[udid]
	if status == "sent" {
		m.SentOK++
		if newSession {
			m.NewSessions++
		}
	} else if status == "failed" {
		m.SentFail++
	}
	m.Total = m.SentOK + m.SentFail
	if taskID != "" {
		m.BatchID = taskID
	}
	m.LastTime = float64(e.now().Unix())
	e.metrics[udid] = m
	e.persistMetricsLocked()
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
			e.report(ItemResult{TaskID: t.TaskID, ItemID: it.ItemID, Phone: it.Phone, Status: "failed", Error: "device ip not configured"})
		}
		return
	}
	e.mu.Lock()
	e.busy[udid] = true
	cancelCh := e.cancel[t.TaskID]
	e.mu.Unlock()
	e.status(DeviceStatus{UDID: udid, WDAStatus: "busy"})
	defer func() {
		e.mu.Lock()
		delete(e.busy, udid)
		delete(e.cancel, t.TaskID)
		e.mu.Unlock()
		e.status(DeviceStatus{UDID: udid, WDAStatus: "online"})
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
		// 发送时间窗：窗口外等待到窗口开始（可被取消中断）。时区由配置 send_timezone 决定。
		loc := e.timeLocation()
		if !withinWindow(sched.WindowStart, sched.WindowEnd, loc) {
			if waitUntilWindow(cancelCh, sched.WindowStart, loc) {
				e.markCancelled(t, it)
				continue
			}
		}
		// 账号预热：设备级按天放量，今日已达上限则停止本任务（剩余明细标记待次日）。
		if sched.WarmUpEnabled {
			days := e.daysActive()
			cap := warmupDailyCap(sched, days)
			if m := e.todayMetrics(udid); m.SentOK >= cap {
				reason := fmt.Sprintf("预热控制：第 %d 天日上限 %d 条已用完，剩余明细未发送（请明日继续）", days, cap)
				e.cancelRemainingReason(t, t.Items[idx:], reason)
				e.status(DeviceStatus{UDID: udid, WDAStatus: "online", Error: reason})
				slog.Warn("warm-up cap reached", "task", t.TaskID, "udid", udid, "day", days, "cap", cap)
				return
			}
		}
		t0 := time.Now()
		status, errMsg := "sent", ""
		assist := e.llmClient()
		if assist != nil && assist.Model == "" {
			assist = nil
		}
		// 模板变量：明细有逐条渲染内容时优先使用（兼容旧平台任务）。
		content := itemContent(t, it)
		// 打开会话（返回是否为新会话），新会话占比控制：超额则该条标记失败待次日，
		// 存量会话不受影响继续发送。
		sid, isNew, oerr := wda.OpenChatForSend(context.Background(), client, it.Phone)
		if oerr != nil {
			status, errMsg = "failed", oerr.Error()
		} else if isNew && sched.MaxNewSessionRatio > 0 {
			m := e.todayMetrics(udid)
			if newSessionRatioExceeded(m.NewSessions, m.Total, sched.MaxNewSessionRatio) {
				status, errMsg = "failed",
					fmt.Sprintf("新会话占比控制:今日新会话占比已达上限 %d%%，该号码请明日再发", sched.MaxNewSessionRatio)
				_ = client.DeleteSession(context.Background(), sid)
				sid = ""
			}
		}
		if sid != "" {
			var serr error
			if assist == nil {
				serr = wda.TypeAndSend(context.Background(), client, sid, content, nil)
			} else {
				serr = wda.TypeAndSend(context.Background(), client, sid, content, assist)
			}
			_ = client.DeleteSession(context.Background(), sid)
			if serr != nil {
				status, errMsg = "failed", serr.Error()
			}
		}
		dur := time.Since(t0).Milliseconds()
		e.persist(t.TaskID, it.ItemID, it.Phone, status, errMsg, dur)
		e.report(ItemResult{TaskID: t.TaskID, ItemID: it.ItemID, Phone: it.Phone, Status: status, Error: errMsg, DurationMs: dur})
		e.recordMetric(udid, t.TaskID, status, isNew && status == "sent")
		slog.Info("item done", "task", t.TaskID, "item", it.ItemID, "phone", it.Phone, "status", status, "new_session", isNew, "duration_ms", dur)

		if status == "failed" {
			// 占比控制的失败不计入连续失败熔断（非设备异常）。
			if strings.Contains(errMsg, "占比控制") {
				continue
			}
			if containsAny(errMsg, "not reachable", "connection", "timed out") {
				slog.Warn("device unreachable, stop task", "task", t.TaskID, "udid", udid)
				return
			}
			consecFails++
			if consecFails >= maxFails {
				e.cancelRemainingReason(t, t.Items[idx+1:], "熔断:连续失败 "+strconv.Itoa(consecFails)+" 条")
				e.status(DeviceStatus{UDID: udid, WDAStatus: "online", Error: fmt.Sprintf("熔断：连续失败 %d 条", consecFails)})
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

// itemContent 返回单条明细的实际发送内容：明细有逐条渲染内容（平台模板变量）
// 时优先使用，否则回退任务级内容（兼容旧平台/旧任务）。
func itemContent(t TaskDispatch, it TaskItem) string {
	if it.Content != "" {
		return it.Content
	}
	return t.Content
}

func (e *Executor) markCancelled(t TaskDispatch, it TaskItem) {
	e.persist(t.TaskID, it.ItemID, it.Phone, "cancelled", "cancelled by platform", 0)
	e.report(ItemResult{TaskID: t.TaskID, ItemID: it.ItemID, Phone: it.Phone, Status: "cancelled", Error: "cancelled by platform"})
}

func (e *Executor) cancelRemainingReason(t TaskDispatch, rest []TaskItem, reason string) {
	for _, it := range rest {
		e.persist(t.TaskID, it.ItemID, it.Phone, "cancelled", reason, 0)
		e.report(ItemResult{TaskID: t.TaskID, ItemID: it.ItemID, Phone: it.Phone, Status: "cancelled", Error: reason})
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

// timeLocation 返回发送时间窗使用的时区（配置 send_timezone；空=本机时区）。
func (e *Executor) timeLocation() *time.Location {
	if e.cfg != nil && e.cfg.Web.SendTimezone != "" {
		if loc, err := time.LoadLocation(e.cfg.Web.SendTimezone); err == nil {
			return loc
		}
		slog.Warn("send_timezone invalid, fallback to local", "tz", e.cfg.Web.SendTimezone)
	}
	return time.Local
}

// withinWindow 判断指定时区当前时间是否在发送窗口内（HH:MM；空=不限制）。
func withinWindow(start, end string, loc *time.Location) bool {
	if start == "" && end == "" {
		return true
	}
	if loc == nil {
		loc = time.Local
	}
	now := time.Now().In(loc)
	hm := now.Hour()*60 + now.Minute()
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
func waitUntilWindow(cancelCh <-chan struct{}, start string, loc *time.Location) bool {
	if start == "" {
		return false
	}
	if loc == nil {
		loc = time.Local
	}
	m := parseHM(start)
	now := time.Now().In(loc)
	next := time.Date(now.Year(), now.Month(), now.Day(), m/60, m%60, 0, 0, loc)
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
			e.report(ItemResult{
				TaskID: taskID, ItemID: itemID,
				Phone:      str(r["phone"]),
				Status:     str(r["status"]),
				Error:      str(r["error"]),
				DurationMs: int64(num(r["duration_ms"])),
			})
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
