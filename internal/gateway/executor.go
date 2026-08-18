package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/url"
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
// 新增字段均 omitempty：平台旧版解析时忽略未知字段，向后兼容。
type ItemResult struct {
	TaskID     string `json:"task_id"`
	ItemID     string `json:"item_id"`
	Phone      string `json:"phone"`
	Status     string `json:"status"`
	Error      string `json:"error"`
	DurationMs int64  `json:"duration_ms"`
	// 设备与内容上下文（数据收集增强）。
	Udid        string `json:"udid,omitempty"`         // 发送设备 UDID
	Serial      string `json:"serial,omitempty"`       // 设备硬件序列号
	DeviceName  string `json:"device_name,omitempty"`  // 设备名称
	ConnType    string `json:"conn_type,omitempty"`    // 连接方式 usb | wifi
	Content     string `json:"content,omitempty"`      // 实际发送内容（逐条渲染后）
	ContactName string `json:"contact_name,omitempty"` // 收件人姓名（best-effort，读不到为空）
	SentAt      string `json:"sent_at,omitempty"`      // 完成时刻 RFC3339（本地时区）
	NewSession  bool   `json:"new_session,omitempty"`  // 该条经「新聊天→搜索」新建会话发送
}

// taskEnv 单次任务执行的设备上下文（随明细/汇总落盘与上行）。
type taskEnv struct {
	Udid       string
	Serial     string
	DeviceName string
	ConnType   string // usb | wifi
}

// TaskSummary 任务级汇总（task:summary 上行 + <task_id>.meta.json 落盘）。
type TaskSummary struct {
	TaskID     string `json:"task_id"`
	Udid       string `json:"udid"`
	Serial     string `json:"serial,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
	ConnType   string `json:"conn_type,omitempty"`
	Status     string `json:"status"` // 见 taskDone/taskStop* 常量
	Total      int    `json:"total"`  // 下发明细总数
	SentOK     int    `json:"sent_ok"`
	SentFail   int    `json:"sent_fail"`
	Cancelled  int    `json:"cancelled"`
	Pending    int    `json:"pending"` // 未执行条数（含待续发）
	StartAt    string `json:"start_at"`
	EndAt      string `json:"end_at"`
	DurationMs int64  `json:"duration_ms"`
	Reason     string `json:"reason,omitempty"`
}

// 任务结束原因（TaskSummary.Status）。
const (
	taskDone        = "completed"
	taskStopNoIP    = "no_ip"              // 设备未配置 IP（无法建立 WDA 通道）
	taskStopUnreach = "device_unreachable" // 设备失联，剩余明细待续发
	taskStopBreaker = "circuit_breaker"    // 连续失败熔断
	taskStopWarmup  = "warmup_cap"         // 预热日上限，剩余明细待次日
)

// DeviceStatus device:status 上行。
type DeviceStatus struct {
	UDID      string `json:"udid"`
	WDAStatus string `json:"wda_status"`
	Error     string `json:"error"`
	ConnType  string `json:"conn_type,omitempty"` // usb | wifi
}

// Executor 按 UDID 串行执行群发任务，先本地持久化结果再上报（at-least-once）。
type Executor struct {
	cfg        *Config
	wda        *WDAManager
	llm        *LLMClient
	llmMu      sync.RWMutex
	resultsDir string
	store      *resultsStore // 明细/汇总/统计持久化（<resultsDir>/results.db，SQLite）

	mu      sync.Mutex
	queues  map[string]chan TaskDispatch
	workers map[string]bool
	cancel  map[string]chan struct{}
	busy    map[string]bool

	ReportQ  chan ItemResult
	StatusQ  chan DeviceStatus
	SummaryQ chan TaskSummary

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
		SummaryQ:       make(chan TaskSummary, 64),
		metrics:        map[string]Metrics{},
		metricsHistory: map[string]Metrics{},
		now:            time.Now,
	}
	e.store = openResultsStore(resultsDir)
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

// loadMetrics 读入历史统计（SQLite results.db metrics 表）。
// 落盘数据标注的日期保持原样（跨天归档由下次 recordMetric 惰性完成），
// 避免启动时刻与数据日期不一致时误归档。
func (e *Executor) loadMetrics() {
	f, ok := e.store.loadMetricsState()
	if !ok {
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

// persistMetricsLocked 落库（SQLite），失败仅告警不影响发送。
func (e *Executor) persistMetricsLocked() {
	f := metricsFileState{Day: e.metricsDay, Devices: e.metrics, History: e.metricsHistory}
	if err := e.store.saveMetricsState(f); err != nil {
		slog.Warn("metrics persist failed", "error", err)
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
// 队列满时丢弃并告警（不阻塞云通道读循环；平台重连后会补推 pending 任务）。
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
		ch = make(chan TaskDispatch, 256)
		e.queues[t.UDID] = ch
	}
	e.cancel[t.TaskID] = make(chan struct{})
	if !e.workers[t.UDID] {
		e.workers[t.UDID] = true
		go e.runUDID(t.UDID)
	}
	e.mu.Unlock()
	// 有界背压：3 秒内排队失败才丢弃并告警（保护云通道读循环不被无限阻塞；
	// 平台重连后会补推 pending 任务）。
	select {
	case ch <- t:
	case <-time.After(3 * time.Second):
		e.mu.Lock()
		delete(e.cancel, t.TaskID)
		e.mu.Unlock()
		slog.Error("dispatch enqueue timeout, task dropped (platform will re-push pending)",
			"task", t.TaskID, "udid", t.UDID)
	}
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
	if udid == "" {
		// 无设备归因的明细不计数：避免产生空 udid 统计桶污染 metrics 设备视图
		// 与 batch_id 归因（旧格式记录的归因由显式 recordMetric 提供）。
		return
	}
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
	start := e.now()
	dev := e.cfg.Device(udid)
	env := taskEnv{Udid: udid, ConnType: connTypeOf(udid)}
	if dev != nil {
		env.Serial, env.DeviceName = dev.Serial, dev.Name
	}
	// 任务收口：无论正常结束/熔断/失联/预热截止，统一统计并上行 task:summary。
	stopStatus, stopReason := taskDone, ""
	defer func() { e.finishTask(env, t, start, stopStatus, stopReason) }()

	ip := ""
	port := 8100
	if dev != nil {
		ip = dev.IP
		port = dev.Port
	}
	if ip == "" {
		for _, it := range t.Items {
			e.finishItem(e.cancelledEnv(env, t, it, "failed", "device ip not configured"))
		}
		stopStatus, stopReason = taskStopNoIP, "device ip not configured"
		return
	}
	e.mu.Lock()
	e.busy[udid] = true
	cancelCh := e.cancel[t.TaskID]
	e.mu.Unlock()
	e.status(DeviceStatus{UDID: udid, WDAStatus: "busy", ConnType: env.ConnType})
	defer func() {
		e.mu.Lock()
		delete(e.busy, udid)
		delete(e.cancel, t.TaskID)
		e.mu.Unlock()
		// 收尾按真实探活上报：无条件报 online 会在设备实际离线时造成 online→offline 抖动。
		final := "online"
		if !e.deviceReachable(udid, ip, port) {
			final = "offline"
		}
		e.status(DeviceStatus{UDID: udid, WDAStatus: final, ConnType: env.ConnType})
	}()

	// 离线设备直接剔除：任务下发时 WDA 探活失败（隧道与 Wi-Fi 均不可达），
	// 可发明细立即标记失败上报，平台可马上转派在线设备；不进逐条执行空耗超时。
	// 缺号码属数据校验（与设备状态无关），仍按「缺少手机号」收口。
	if !e.deviceReachable(udid, ip, port) {
		reason := "设备离线（WDA 不可达），任务被网关拒绝，请转派在线设备后重试"
		offline := false
		for _, it := range t.Items {
			if strings.TrimSpace(it.Phone) == "" {
				e.rejectEmptyPhone(env, t, it, udid)
				continue
			}
			e.finishItem(e.cancelledEnv(env, t, it, "failed", reason))
			offline = true
		}
		if offline {
			e.status(DeviceStatus{UDID: udid, WDAStatus: "offline", Error: reason, ConnType: env.ConnType})
			slog.Warn("task rejected: device offline", "task", t.TaskID, "udid", udid, "items", len(t.Items))
			stopStatus, stopReason = taskStopUnreach, reason
		}
		return
	}

	sched := t.Schedule
	maxFails := sched.MaxConsecutiveFails
	if maxFails <= 0 {
		maxFails = 5
	}
	client := wda.NewClient(wdaBaseURLFor(udid, ip, port), 40*time.Second)
	consecFails := 0

	for idx, it := range t.Items {
		select {
		case <-cancelCh:
			e.markCancelled(env, t, it)
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
				e.markCancelled(env, t, it)
				continue
			}
		}
		// 账号预热：设备级按天放量，今日已达上限则停止本任务（剩余明细标记待次日）。
		if sched.WarmUpEnabled {
			days := e.daysActive()
			cap := warmupDailyCap(sched, days)
			if m := e.todayMetrics(udid); m.SentOK >= cap {
				reason := fmt.Sprintf("预热控制：第 %d 天日上限 %d 条已用完，剩余明细未发送（请明日继续）", days, cap)
				e.cancelRemainingReason(env, t, t.Items[idx:], reason)
				e.status(DeviceStatus{UDID: udid, WDAStatus: "online", Error: reason, ConnType: env.ConnType})
				slog.Warn("warm-up cap reached", "task", t.TaskID, "udid", udid, "day", days, "cap", cap)
				stopStatus, stopReason = taskStopWarmup, reason
				return
			}
		}
		t0 := time.Now()
		status, errMsg := "sent", ""
		contactName := ""
		assist := e.llmClient()
		if assist != nil && assist.Model == "" {
			assist = nil
		}
		// 模板变量：明细有逐条渲染内容时优先使用（兼容旧平台任务）。
		content := itemContent(t, it)
		// 手机号缺失：禁止无号码发送——旧逻辑会落到「默认会话」把消息发给
		// 当前打开的任意聊天，造成明细显示发送成功却无手机号。此类明细直接标记失败。
		if strings.TrimSpace(it.Phone) == "" {
			e.rejectEmptyPhone(env, t, it, udid)
			continue
		}
		// 打开会话（返回是否为新会话），新会话占比控制：超额则该条标记失败待次日，
		// 存量会话不受影响继续发送。
		// 可达性类瞬时故障（WDA 500/超时/连接抖动）先原地重试一次再判失败——
		// 打开会话本身幂等（未发送任何内容），重试无重复发送风险。
		sid, isNew, oerr := wda.OpenChatForSend(context.Background(), client, it.Phone)
		if oerr != nil && transientWDAError(oerr) {
			slog.Warn("open chat transient error, retry once", "task", t.TaskID, "item", it.ItemID, "error", oerr.Error())
			time.Sleep(2 * time.Second)
			sid, isNew, oerr = wda.OpenChatForSend(context.Background(), client, it.Phone)
		}
		if oerr != nil {
			status, errMsg = "failed", oerr.Error()
		} else {
			// 收件人姓名：聊天页已打开，尽力读标题（联系人名/号码），失败为空不影响发送。
			contactName = wda.ChatTitle(context.Background(), client, sid)
			if isNew && sched.MaxNewSessionRatio > 0 {
				m := e.todayMetrics(udid)
				if newSessionRatioExceeded(m.NewSessions, m.Total, sched.MaxNewSessionRatio) {
					status, errMsg = "failed",
						fmt.Sprintf("新会话占比控制:今日新会话占比已达上限 %d%%，该号码请明日再发", sched.MaxNewSessionRatio)
					_ = client.DeleteSession(context.Background(), sid)
					sid = ""
				}
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
		e.finishItem(ItemResult{
			TaskID: t.TaskID, ItemID: it.ItemID, Phone: it.Phone, Status: status, Error: errMsg, DurationMs: dur,
			Udid: env.Udid, Serial: env.Serial, DeviceName: env.DeviceName, ConnType: env.ConnType,
			Content: content, ContactName: contactName, SentAt: e.now().Format(time.RFC3339), NewSession: isNew,
		})
		slog.Info("item done", "task", t.TaskID, "item", it.ItemID, "phone", it.Phone, "status", status,
			"new_session", isNew, "contact", contactName, "conn", env.ConnType, "duration_ms", dur)

		if status == "failed" {
			// 占比控制的失败不计入连续失败熔断（非设备异常）。
			if strings.Contains(errMsg, "占比控制") {
				continue
			}
			if containsAny(errMsg, "not reachable", "connection", "timed out") {
				slog.Warn("device unreachable, stop task", "task", t.TaskID, "udid", udid)
				stopStatus, stopReason = taskStopUnreach, "设备失联（WDA 不可达/超时），剩余明细待平台续发"
				return
			}
			consecFails++
			if consecFails >= maxFails {
				reason := "熔断:连续失败 " + strconv.Itoa(consecFails) + " 条"
				e.cancelRemainingReason(env, t, t.Items[idx+1:], reason)
				e.status(DeviceStatus{UDID: udid, WDAStatus: "online", Error: reason, ConnType: env.ConnType})
				slog.Warn("circuit breaker triggered", "task", t.TaskID, "udid", udid, "consecutive_fails", consecFails)
				stopStatus, stopReason = taskStopBreaker, reason
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

// deviceReachable 任务开始前快速探活设备 WDA（USB 隧道优先，Wi-Fi 兜底）。
func (e *Executor) deviceReachable(udid, ip string, port int) bool {
	u, err := url.Parse(wdaBaseURLFor(udid, ip, port))
	if err != nil {
		return false
	}
	p, _ := strconv.Atoi(u.Port())
	if p == 0 {
		p = 8100
	}
	return CheckWDA(u.Hostname(), p, 4*time.Second).OK
}

// rejectEmptyPhone 无号码明细直接标记失败（数据校验不依赖设备状态，
// 离线剔除时同样先按此收口，保证平台看到的失败原因一致）。
func (e *Executor) rejectEmptyPhone(env taskEnv, t TaskDispatch, it TaskItem, udid string) {
	e.finishItem(e.cancelledEnv(env, t, it, "failed", "明细缺少手机号，已拒绝发送（无号码禁止发送到默认会话）"))
	slog.Warn("item rejected: empty phone", "task", t.TaskID, "item", it.ItemID)
}

// itemContent 返回单条明细的实际发送内容：明细有逐条渲染内容（平台模板变量）
// 时优先使用，否则回退任务级内容（兼容旧平台/旧任务）。
func itemContent(t TaskDispatch, it TaskItem) string {
	if it.Content != "" {
		return it.Content
	}
	return t.Content
}

// cancelledEnv 构造一条带设备上下文与计划内容的明细结果（status/error 由调用方给定）。
func (e *Executor) cancelledEnv(env taskEnv, t TaskDispatch, it TaskItem, status, errMsg string) ItemResult {
	return ItemResult{
		TaskID: t.TaskID, ItemID: it.ItemID, Phone: it.Phone, Status: status, Error: errMsg,
		Udid: env.Udid, Serial: env.Serial, DeviceName: env.DeviceName, ConnType: env.ConnType,
		Content: itemContent(t, it), SentAt: e.now().Format(time.RFC3339),
	}
}

func (e *Executor) markCancelled(env taskEnv, t TaskDispatch, it TaskItem) {
	e.finishItem(e.cancelledEnv(env, t, it, "cancelled", "cancelled by platform"))
}

func (e *Executor) cancelRemainingReason(env taskEnv, t TaskDispatch, rest []TaskItem, reason string) {
	for _, it := range rest {
		e.finishItem(e.cancelledEnv(env, t, it, "cancelled", reason))
	}
}

// connTypeOf 返回设备当前连接方式：USB 直连为 "usb"，否则视为 Wi-Fi 通道 "wifi"。
func connTypeOf(udid string) string {
	for _, u := range USBUDIDs() {
		if u == udid {
			return "usb"
		}
	}
	return "wifi"
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

// ---- 本地持久化（at-least-once，SQLite results.db）----

// itemRecord 是单条明细的落盘结构（与旧 JSON 文件兼容，导入沿用）。
type itemRecord struct {
	Phone       string `json:"phone"`
	Status      string `json:"status"`
	Error       string `json:"error"`
	DurationMs  int64  `json:"duration_ms"`
	Udid        string `json:"udid,omitempty"`
	Serial      string `json:"serial,omitempty"`      // 设备硬件序列号（补报上行不丢字段）
	DeviceName  string `json:"device_name,omitempty"` // 设备名称
	ConnType    string `json:"conn_type,omitempty"`
	Content     string `json:"content,omitempty"`
	ContactName string `json:"contact_name,omitempty"`
	SentAt      string `json:"sent_at,omitempty"`
	NewSession  bool   `json:"new_session,omitempty"`
}

func (e *Executor) persisted(taskID, itemID string) bool {
	return e.store.itemPersisted(taskID, itemID)
}

// finishItem 落盘并上报单条明细（先落盘后上报，断网不丢）。
// 发送计数在此单点收敛（sent/failed 均计入 metrics），保证落盘明细与统计永远一致。
func (e *Executor) finishItem(r ItemResult) {
	e.persistItem(r)
	if r.Status == "sent" || r.Status == "failed" {
		e.recordMetric(r.Udid, r.TaskID, r.Status, r.Status == "sent" && r.NewSession)
	}
	e.report(r)
}

func (e *Executor) persistItem(r ItemResult) {
	_ = e.store.putItem(r.TaskID, r.ItemID, itemRecord{
		Phone: r.Phone, Status: r.Status, Error: r.Error, DurationMs: r.DurationMs,
		Udid: r.Udid, Serial: r.Serial, DeviceName: r.DeviceName, ConnType: r.ConnType,
		Content: r.Content, ContactName: r.ContactName, SentAt: r.SentAt, NewSession: r.NewSession,
	}, e.now())
}

// finishTask 任务收口：按已落盘明细统计，写 meta 并上行 task:summary（meta 落库后队列满可丢，重连补报）。
func (e *Executor) finishTask(env taskEnv, t TaskDispatch, start time.Time, status, reason string) {
	if status == "" {
		status = taskDone
	}
	m := e.readItems(t.TaskID)
	var ok, fail, cancel int
	for _, r := range m {
		switch r.Status {
		case "sent":
			ok++
		case "failed":
			fail++
		case "cancelled":
			cancel++
		}
	}
	end := e.now()
	s := TaskSummary{
		TaskID: t.TaskID, Udid: env.Udid, Serial: env.Serial, DeviceName: env.DeviceName, ConnType: env.ConnType,
		Status: status, Total: len(t.Items), SentOK: ok, SentFail: fail, Cancelled: cancel,
		Pending: len(t.Items) - ok - fail - cancel,
		StartAt: start.Format(time.RFC3339), EndAt: end.Format(time.RFC3339),
		DurationMs: end.Sub(start).Milliseconds(), Reason: reason,
	}
	if err := e.store.putMeta(s); err != nil {
		slog.Warn("task meta persist failed", "task", s.TaskID, "error", err)
	}
	select {
	case e.SummaryQ <- s:
	default:
		slog.Warn("summary queue full, dropped (persisted, will re-report)", "task", s.TaskID)
	}
}

// readItems 读入某任务的全部已落盘明细。
func (e *Executor) readItems(taskID string) map[string]itemRecord {
	return e.store.items(taskID)
}

// readSummary 读入某任务已落盘的汇总（无则返回 nil）。
func (e *Executor) readSummary(taskID string) *TaskSummary {
	return e.store.meta(taskID)
}

// TaskListItem 是 /api/tasks 的列表项。
type TaskListItem struct {
	TaskID    string       `json:"task_id"`
	Finished  bool         `json:"finished"` // 任务已收口（meta 已写）
	Summary   *TaskSummary `json:"summary,omitempty"`
	SentOK    int          `json:"sent_ok"`
	SentFail  int          `json:"sent_fail"`
	Cancelled int          `json:"cancelled"`
	Items     int          `json:"items"`      // 已落盘明细数
	UpdatedAt int64        `json:"updated_at"` // 明细文件最后修改时间（Unix 秒）
}

// TaskList 返回本地已持久化任务（按更新时间倒序，最多 100 个）。
func (e *Executor) TaskList() []TaskListItem {
	var out []TaskListItem
	for _, t := range e.store.taskIDsByUpdate(100) {
		item := TaskListItem{TaskID: t.TaskID, UpdatedAt: t.UpdatedAt}
		if s := e.readSummary(t.TaskID); s != nil {
			item.Summary, item.Finished = s, true
		}
		item.SentOK, item.SentFail, item.Cancelled, item.Items = e.store.taskStats(t.TaskID)
		out = append(out, item)
	}
	return out
}

// ItemDetail 是 /api/tasks/{task_id} 的单条明细（落盘结构 + item_id）。
type ItemDetail struct {
	ItemID string `json:"item_id"`
	itemRecord
}

// TaskDetail 分页返回某任务明细（按 item_id 排序；limit<=0 时默认 500）。
func (e *Executor) TaskDetail(taskID string, offset, limit int) ([]ItemDetail, int) {
	m := e.readItems(taskID)
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 500
	}
	if offset > len(ids) {
		offset = len(ids)
	}
	end := offset + limit
	if end > len(ids) {
		end = len(ids)
	}
	out := make([]ItemDetail, 0, end-offset)
	for _, id := range ids[offset:end] {
		out = append(out, ItemDetail{ItemID: id, itemRecord: m[id]})
	}
	return out, len(ids)
}

// DeviceItem 跨任务视图的单条明细（含 task_id，便于回平台核对）。
type DeviceItem struct {
	TaskID string `json:"task_id"`
	ItemDetail
}

// DeviceItemGroup 是 /api/items 的按设备分组视图；udid 为空 = 无法归因的历史记录。
type DeviceItemGroup struct {
	Udid      string       `json:"udid"`
	Serial    string       `json:"serial,omitempty"`
	Name      string       `json:"name,omitempty"`
	SentOK    int          `json:"sent_ok"`
	SentFail  int          `json:"sent_fail"`
	Cancelled int          `json:"cancelled"`
	Items     []DeviceItem `json:"items"`
}

// DeviceItems 汇总所有任务落盘明细，按设备分组、最新活跃设备在前（每组内按时间倒序）。
// 历史明细缺 udid 时按 metrics 的 batch_id→udid 映射尽力归因
// （该映射只保留每设备最近一批任务），仍无法归因的归入 udid 为空的「未知设备」组。
// udidFilter 非空时只返回该设备；limit<=0 默认 3000 条；第二返回值为是否已截断。
func (e *Executor) DeviceItems(udidFilter string, limit int) ([]DeviceItemGroup, bool) {
	if limit <= 0 {
		limit = 3000
	}
	batchUDID := map[string]string{}
	e.metricsMu.Lock()
	for u, m := range e.metrics {
		if m.BatchID != "" {
			batchUDID[m.BatchID] = u
		}
	}
	e.metricsMu.Unlock()

	type rec struct {
		TaskID string
		ItemID string
		itemRecord
		mtime time.Time
	}
	var all []rec
	for _, row := range e.store.recentItems(limit + 500) { // 略取宽裕量，过滤/排序后截断
		if udidFilter != "" {
			udid := row.Record.Udid
			if udid == "" {
				udid = batchUDID[row.TaskID]
			}
			if udid != udidFilter {
				continue
			}
		}
		all = append(all, rec{TaskID: row.TaskID, ItemID: row.ItemID, itemRecord: row.Record, mtime: row.UpdatedAt})
	}
	itemTime := func(r rec) time.Time {
		if r.SentAt != "" {
			if ts, err := time.Parse(time.RFC3339, r.SentAt); err == nil {
				return ts
			}
		}
		return r.mtime
	}
	sort.Slice(all, func(i, j int) bool {
		ti, tj := itemTime(all[i]), itemTime(all[j])
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return all[i].ItemID > all[j].ItemID
	})
	truncated := len(all) > limit
	if truncated {
		all = all[:limit]
	}

	groups := map[string]*DeviceItemGroup{}
	var order []string
	for _, r := range all {
		udid := r.Udid
		if udid == "" {
			udid = batchUDID[r.TaskID]
		}
		g := groups[udid]
		if g == nil {
			g = &DeviceItemGroup{Udid: udid}
			if e.cfg != nil {
				if dev := e.cfg.Device(udid); dev != nil {
					g.Serial, g.Name = dev.Serial, dev.Name
				}
			}
			// 设备已从配置移除时，用明细记录里落盘的序列号/名称兜底，分组标识不缺字段。
			if g.Serial == "" && r.Serial != "" {
				g.Serial = r.Serial
			}
			if g.Name == "" && r.DeviceName != "" {
				g.Name = r.DeviceName
			}
			groups[udid] = g
			order = append(order, udid)
		}
		g.Items = append(g.Items, DeviceItem{TaskID: r.TaskID, ItemDetail: ItemDetail{ItemID: r.ItemID, itemRecord: r.itemRecord}})
		switch r.Status {
		case "sent":
			g.SentOK++
		case "failed":
			g.SentFail++
		case "cancelled":
			g.Cancelled++
		}
	}
	out := make([]DeviceItemGroup, 0, len(order))
	for _, u := range order {
		if u != "" {
			out = append(out, *groups[u])
		}
	}
	if g := groups[""]; g != nil {
		out = append(out, *g) // 未知设备组放最后
	}
	return out, truncated
}

// ResendPersisted 重连后补报本地已持久化的明细与任务汇总（SQLite results.db）。
func (e *Executor) ResendPersisted() {
	// 任务汇总（meta）
	for _, t := range e.store.taskIDsByUpdate(0) { // 0 = 不限量
		if s := e.readSummary(t.TaskID); s != nil {
			select {
			case e.SummaryQ <- *s:
			default:
			}
		}
	}
	// 全部明细（老记录缺 serial/device_name 时按 udid 从配置兜底补全，保证上行字段完整）
	for _, row := range e.store.recentItems(0) { // 0 = 不限量
		r := row.Record
		serial, devName := r.Serial, r.DeviceName
		if e.cfg != nil && r.Udid != "" {
			if dev := e.cfg.Device(r.Udid); dev != nil {
				if serial == "" {
					serial = dev.Serial
				}
				if devName == "" {
					devName = dev.Name
				}
			}
		}
		e.report(ItemResult{
			TaskID: row.TaskID, ItemID: row.ItemID,
			Phone: r.Phone, Status: r.Status, Error: r.Error, DurationMs: r.DurationMs,
			Udid: r.Udid, Serial: serial, DeviceName: devName, ConnType: r.ConnType, Content: r.Content,
			ContactName: r.ContactName, SentAt: r.SentAt, NewSession: r.NewSession,
		})
	}
}

// transientWDAError 判断错误是否为可达性类瞬时故障（WDA 5xx/超时/连接抖动），
// 这类错误值得原地重试一次；元素找不到等业务性错误不属于此类。
func transientWDAError(err error) bool {
	if err == nil {
		return false
	}
	return containsAny(err.Error(),
		"not reachable", "connection", "timed out", "timeout", "deadline exceeded", "EOF")
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
