package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// AutonomyLoop 是「deepseek-harness 核心 × 群发」的实现：
// tick -> 观察(只读) -> 确定性预筛(没有群发不触发事件，不调模型) -> 模型结构化决策 ->
// 单调守卫 -> submit_batch(发应用联系人) -> 会话日志。
type AutonomyLoop struct {
	g *Gateway
	// llm 是决策模型客户端（与发送兜底共用配置）；为空或不可用时自主功能静默关闭。
	llm *LLMClient
	now func() time.Time
	st  agentState

	startOnce sync.Once
	cancel    context.CancelFunc

	running       atomic.Bool
	lastReady     int
	diagMu        sync.Mutex
	diag          autonomyDiagnosis

	lastMu sync.Mutex
	last   lastAgentTask
}

// lastAgentTask 记录最近一次提交的自主任务（用于把执行器真实结果回填到状态）。
type lastAgentTask struct {
	TaskID string
	Udid   string
	At     time.Time
}

// agentState 记录自主任务当日在每台设备的提交进度，用于“本地优先/当日去重”。
type agentState struct {
	mu        sync.Mutex
	day       string
	submitted map[string]string // udid -> taskID
}

// agentCandidate 是一台可发（就绪、预算未满、当日未提交）的设备。
type agentCandidate struct {
	UDID   string
	Remain int
}

// agentSubmission 是守卫通过后、即将提交的设备与话术。
type agentSubmission struct {
	UDID    string
	Content string
}

// agentDeviceView 是模型可感知的某台设备状态快照（不可信前只读事实）。
type agentDeviceView struct {
	UDID      string `json:"udid"`
	IP        string `json:"ip"`
	Healthy   bool   `json:"healthy"`
	Status    string `json:"status"`
	TodaySent int    `json:"today_sent"`
	TodayFail int    `json:"today_fail"`
	TodayNew  int    `json:"today_new"`
	Remain    int    `json:"remain"`
}

// agentStateView 是一次「观察」得到的网关/设备状态摘要（喂给模型）。
type agentStateView struct {
	Now        string             `json:"now"`
	Timezone   string             `json:"timezone"`
	InWindow   bool               `json:"in_window"`
	Window     string             `json:"window"`
	Devices    []agentDeviceView  `json:"devices"`
	Content    string             `json:"content"`
	DailyCap   int                `json:"daily_cap"`
	MaxFriends int                `json:"max_friends"`
	Interval   int                `json:"interval_sec"`
	Burst      string             `json:"burst"`
}

// NewAutonomyLoop 构造自主回路。
func NewAutonomyLoop(g *Gateway) *AutonomyLoop {
	return &AutonomyLoop{g: g, llm: g.LLM, now: time.Now, st: agentState{submitted: map[string]string{}}}
}

// Start 启动自主回路（幂等）；ctx 取消时停止。
func (a *AutonomyLoop) Start(ctx context.Context) {
	a.startOnce.Do(func() {
		cctx, cancel := context.WithCancel(ctx)
		a.cancel = cancel
		a.running.Store(true)
		go func() {
			defer a.running.Store(false)
			a.loop(cctx)
		}()
	})
}

func (a *AutonomyLoop) loop(ctx context.Context) {
	interval := a.tickInterval()
	t := time.NewTicker(interval)
	defer t.Stop()
	if err := a.step(ctx); err != nil {
		slog.Warn("autonomy step", "error", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := a.step(ctx); err != nil {
				slog.Warn("autonomy step", "error", err)
			}
		}
	}
}

func (a *AutonomyLoop) tickInterval() time.Duration {
	plan := a.normalizePlan(a.cfg().Autonomy)
	return time.Duration(plan.TickInterval) * time.Second
}

func (a *AutonomyLoop) cfg() *Config { return a.g.Cfg }

// normalizePlan 填充默认值（配置只存显式字段，零值在此补齐）。
func (a *AutonomyLoop) normalizePlan(p AutonomyConfig) AutonomyConfig {
	if p.TickInterval <= 0 {
		p.TickInterval = 60
	}
	if p.IntervalSec <= 0 {
		p.IntervalSec = 20
	}
	if p.BurstCount <= 0 {
		p.BurstCount = 5
	}
	if p.BurstPauseSec <= 0 {
		p.BurstPauseSec = 60
	}
	if p.DailyCap <= 0 {
		p.DailyCap = 40
	}
	if p.MaxNewSessionRatio <= 0 {
		p.MaxNewSessionRatio = 30
	}
	if p.MaxFriends <= 0 {
		p.MaxFriends = 30
	}
	return p
}

// step 执行一轮自主回路。任一步骤判定“没有群发”即静默返回（不调模型、不写决策事件）。
func (a *AutonomyLoop) step(ctx context.Context) error {
	plan := a.normalizePlan(a.cfg().Autonomy)
	if !plan.Enabled {
		a.setDiag("disabled", "自主群发未启用（请开启并保存话术）")
		return nil
	}
	st := a.observe(plan)
	a.setReady(len(st.Devices))
	if !st.InWindow {
		win := plan.WindowStart + "~" + plan.WindowEnd
		if win == "~" {
			win = "全天"
		}
		a.setDiag("outside_window", "发送窗口外（" + win + "）")
		return nil
	}
	if len(st.Devices) == 0 {
		if a.onlineDeviceCount() > 0 {
			a.setDiag("budget_reached", "设备在线但今日预算已满（每日上限 "+fmt.Sprintf("%d", plan.DailyCap)+" 条）")
		} else {
			a.setDiag("no_device", "没有在线且预算未满的设备（请检查 WDA 是否在线）")
		}
		return nil
	}
	targets := a.prefilter(plan, st)
	if len(targets) == 0 {
		a.setDiag("already_sent", "今日这些设备已发过一批，重复触达间隔内不再重发")
		return nil
	}
	sub, ok, how := a.decide(ctx, plan, st, targets)
	if !ok {
		a.logEvent("guard", map[string]any{"result": "reject", "reason": how})
		a.setDiag("rejected", "发送被拒绝：" + how)
		return nil
	}
	taskID := a.submit(plan, sub)
	a.st.markSubmitted(sub.UDID, taskID, a.now())
	a.logEvent("submit", map[string]any{"udid": sub.UDID, "task": taskID, "mode": how})
	a.setDiag("submitted", "已提交自主任务 "+taskID+"（"+how+"）；若今日已发数未增长，可能是没有新的未触达联系人或发送失败，请查看发送明细")
	slog.Info("autonomy submitted", "udid", sub.UDID, "task", taskID, "mode", how)
	return nil
}

// decide 执行一次决策：优先用 LLM 结构化决定；LLM 不可用/失败时确定性兜底（不静默停摆）。
func (a *AutonomyLoop) decide(ctx context.Context, plan AutonomyConfig, st agentStateView, targets []agentCandidate) (agentSubmission, bool, string) {
	if a.llm == nil || !a.llm.Enabled() {
		s, ok := a.fallback(plan, targets)
		return s, ok, "llm off, fallback"
	}
	sys, user := a.prompt(st, targets)
	decision, err := a.llm.Decide(ctx, sys, user)
	if err != nil {
		s, ok := a.fallback(plan, targets)
		return s, ok, "llm error, fallback: " + err.Error()
	}
	s, ok := a.guard(plan, decision, targets)
	if !ok {
		return s, false, "模型决策被拒"
	}
	return s, true, "llm"
}

// fallback 确定性兜底：选剩余预算最大的一台就绪设备直接发（模型不可用时自主功能仍工作）。
func (a *AutonomyLoop) fallback(plan AutonomyConfig, targets []agentCandidate) (agentSubmission, bool) {
	if len(targets) == 0 {
		return agentSubmission{}, false
	}
	best, maxRemain := 0, -1
	for i, c := range targets {
		if c.Remain > maxRemain {
			best, maxRemain = i, c.Remain
		}
	}
	if maxRemain < 0 {
		return agentSubmission{}, false
	}
	return agentSubmission{UDID: targets[best].UDID, Content: plan.Content}, true
}

// observe 采集只读状态：窗口、就绪/健康设备、今日统计与剩余预算。
func (a *AutonomyLoop) observe(plan AutonomyConfig) agentStateView {
	loc := a.g.Exec.timeLocation()
	now := a.now().In(loc)
	st := agentStateView{
		Now: now.Format(time.RFC3339), Timezone: loc.String(),
		InWindow: withinWindowAt(plan.WindowStart, plan.WindowEnd, loc, a.now()),
		Window:   fmt.Sprintf("%s-%s", plan.WindowStart, plan.WindowEnd),
		Content:  plan.Content, DailyCap: plan.DailyCap, MaxFriends: plan.MaxFriends,
		Interval: plan.IntervalSec,
		Burst:    fmt.Sprintf("%d/%d", plan.BurstCount, plan.BurstPauseSec),
	}
	for i := range a.cfg().Devices {
		d := &a.cfg().Devices[i]
		if d.UDID == "" || d.IP == "" || a.g.Exec.IsBusy(d.UDID) || !healthOK(d.LastHealth) {
			continue
		}
		m := a.g.Exec.Metrics(d.UDID)
		remain := plan.DailyCap - m.SentOK
		if remain <= 0 {
			continue
		}
		st.Devices = append(st.Devices, agentDeviceView{
			UDID: d.UDID, IP: d.IP, Healthy: true, Status: "online",
			TodaySent: m.SentOK, TodayFail: m.SentFail, TodayNew: m.NewSessions, Remain: remain,
		})
	}
	return st
}

// onlineDeviceCount 统计在线且非忙碌的设备数（不论预算），用于区分“无设备”与“预算已满”。
func (a *AutonomyLoop) onlineDeviceCount() int {
	n := 0
	for i := range a.cfg().Devices {
		d := &a.cfg().Devices[i]
		if d.UDID == "" || d.IP == "" || a.g.Exec.IsBusy(d.UDID) || !healthOK(d.LastHealth) {
			continue
		}
		n++
	}
	return n
}

// prefilter 确定性预筛：去掉当日已提交的设备（满足“没有群发不触发事件”）。
func (a *AutonomyLoop) prefilter(plan AutonomyConfig, st agentStateView) []agentCandidate {
	now := a.now()
	var out []agentCandidate
	for _, d := range st.Devices {
		if a.st.submittedToday(d.UDID, now) {
			continue
		}
		out = append(out, agentCandidate{UDID: d.UDID, Remain: d.Remain})
	}
	return out
}

// prompt 组装决策提示词：只读状态 + 严格 JSON 输出约束。
func (a *AutonomyLoop) prompt(st agentStateView, targets []agentCandidate) (string, string) {
	system := `你是微信群发网关的自主群发决策器。根据给定的设备状态，决定是否对某台设备发起一轮
向 WhatsApp 应用联系人（聊天列表 1:1 好友）的群发。只回复一个 JSON 对象，不要 Markdown、不要多余文字：
{"intent":"idle|submit","udid":"...","reason":"..."}
规则：intent 只能取 idle 或 submit；submit 时 udid 必须来自就绪设备列表；拿不准就选 idle。
不要编造话术、不要给号码、不要提出窗口或预算之外的发送。`
	b, _ := json.Marshal(struct {
		State   agentStateView `json:"state"`
		Targets []agentCandidate `json:"targets"`
	}{State: st, Targets: targets})
	return system, string(b)
}

// guard 单调守卫：模型输出不可信，越权/越预算/坏 intent 一律拒绝，不真实发送。
func (a *AutonomyLoop) guard(plan AutonomyConfig, dec map[string]any, targets []agentCandidate) (agentSubmission, bool) {
	intent, _ := dec["intent"].(string)
	if intent != "submit" {
		return agentSubmission{}, false
	}
	if len(targets) == 0 {
		return agentSubmission{}, false
	}
	udid, _ := dec["udid"].(string)
	if udid == "" {
		return agentSubmission{}, false
	}
	found := false
	for _, c := range targets {
		if c.UDID == udid {
			found = true
			break
		}
	}
	if !found {
		return agentSubmission{}, false
	}
	content := strings.TrimSpace(plan.Content)
	if content == "" || len(content) > 1000 {
		return agentSubmission{}, false
	}
	return agentSubmission{UDID: udid, Content: content}, true
}

// submit 构造本地优先的 TaskDispatch 送入 Executor（内容固定取自 plan，模型无法改写）。
func (a *AutonomyLoop) submit(plan AutonomyConfig, sub agentSubmission) string {
	// 预算硬约束：单次发送上限 = min(MaxFriends, 今日剩余预算)。剩余含云任务已发，绝不超发。
	batch := autonomyBatch(plan.DailyCap, plan.MaxFriends, a.g.Exec.Metrics(sub.UDID).SentOK)
	taskID := autoTaskID(sub.UDID, sub.Content, a.now())
	dispatch := TaskDispatch{
		TaskID: taskID, UDID: sub.UDID, Source: "agent",
		Content: sub.Content, IntervalSec: plan.IntervalSec, MaxFriends: batch,
		Schedule: GatewaySchedule{
			WindowStart: plan.WindowStart, WindowEnd: plan.WindowEnd,
			BurstCount: plan.BurstCount, BurstPauseSec: plan.BurstPauseSec,
			MaxConsecutiveFails: 5, MaxNewSessionRatio: plan.MaxNewSessionRatio,
		},
	}
	a.g.Exec.Submit(dispatch)
	a.setLastTask(sub.UDID, taskID)
	return taskID
}

func (a *AutonomyLoop) setLastTask(udid, taskID string) {
	a.lastMu.Lock()
	defer a.lastMu.Unlock()
	a.last = lastAgentTask{TaskID: taskID, Udid: udid, At: a.now()}
}

// lastTaskResult 读取最近一次自主任务的执行结果（从 results.db 回填，避免“猜”）。
func (a *AutonomyLoop) lastTaskResult() (taskID, desc string, ok bool) {
	a.lastMu.Lock()
	t := a.last
	a.lastMu.Unlock()
	if t.TaskID == "" {
		return "", "", false
	}
	items := a.g.Exec.readItems(t.TaskID)
	rec, found := items["chat-list"]
	if !found {
		return t.TaskID, "发送中…", true
	}
	switch rec.Status {
	case "sent":
		if rec.ChatListSent > 0 {
			return t.TaskID, fmt.Sprintf("已发送 %d 个应用联系人", rec.ChatListSent), true
		}
		return t.TaskID, "已发送", true
	case "failed":
		if strings.Contains(rec.Error, "未找到新的") {
			return t.TaskID, "没有新的未触达联系人（均已发过或在重复触达间隔内）", true
		}
		return t.TaskID, "发送失败：" + rec.Error, true
	case "cancelled":
		return t.TaskID, "已取消", true
	default:
		return t.TaskID, "状态：" + rec.Status, true
	}
}

// autonomyBatch 预算硬约束：单批 = min(MaxFriends, 今日剩余预算)；最少 1，绝不超发。
func autonomyBatch(dailyCap, maxFriends, todaySent int) int {
	remaining := dailyCap - todaySent
	if remaining <= 0 {
		remaining = 1
	}
	b := maxFriends
	if remaining < b {
		b = remaining
	}
	if b <= 0 {
		b = 1
	}
	return b
}

func (s *agentState) markSubmitted(udid, taskID string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	day := now.Format("2006-01-02")
	if s.day != day {
		s.submitted = map[string]string{}
		s.day = day
	}
	s.submitted[udid] = taskID
}

func (s *agentState) submittedToday(udid string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.day != now.Format("2006-01-02") {
		return false
	}
	_, ok := s.submitted[udid]
	return ok
}

// logEvent 追加一条会话日志（审计/回放；不写 api_key，完整话术由调用方脱敏）。
func (a *AutonomyLoop) logEvent(kind string, extra map[string]any) {
	dir := filepath.Join(a.cfg().Dir(), "data", "agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	m := map[string]any{"seq": time.Now().UnixNano(), "time": a.now().Format(time.RFC3339), "kind": kind}
	for k, v := range extra {
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "history-"+a.now().Format("2006-01-02")+".jsonl"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

// autoTaskID 生成确定性任务 id：同一天同设备同话术 -> 同 id（Executor 幂等，不会双发）。
func autoTaskID(udid, content string, now time.Time) string {
	h := sha256.Sum256([]byte(udid + "|" + now.Format("2006-01-02") + "|" + content))
	return "auto-" + hex.EncodeToString(h[:])[:20]
}

// autonomyDiagnosis 记录上次自主群发为何未发/已发（供运营排查）。
type autonomyDiagnosis struct {
	State    string `json:"state"`
	Reason   string `json:"reason"`
	LastTick string `json:"last_tick"`
}

// autonomyStatus 是 /api/autonomy/status 的视图：开关、运行态、预算、窗口、就绪设备、诊断原因。
type autonomyStatus struct {
	Enabled      bool   `json:"enabled"`
	Running      bool   `json:"running"`
	LLMEnabled   bool   `json:"llm_enabled"`
	State        string `json:"state"`
	LastTask       string `json:"last_task"`
	LastTaskResult string `json:"last_task_result"`
	TodaySent    int    `json:"today_sent"`
	DailyCap     int    `json:"daily_cap"`
	MaxFriends   int    `json:"max_friends"`
	Window       string `json:"window"`
	InWindow     bool   `json:"in_window"`
	ReadyDevices int    `json:"ready_devices"`
	Reason       string `json:"reason"`
	LastTick     string `json:"last_tick"`
}

func (a *AutonomyLoop) setDiag(state, reason string) {
	a.diagMu.Lock()
	a.diag.State = state
	a.diag.Reason = reason
	a.diag.LastTick = a.now().Format(time.RFC3339)
	a.diagMu.Unlock()
}

func (a *AutonomyLoop) setReady(n int) {
	a.diagMu.Lock()
	a.lastReady = n
	a.diagMu.Unlock()
}

// Status 返回自主群发当前状态与“为何未发”诊断。
func (a *AutonomyLoop) Status() autonomyStatus {
	plan := a.normalizePlan(a.cfg().Autonomy)
	a.diagMu.Lock()
	ready := a.lastReady
	reason, last, state := a.diag.Reason, a.diag.LastTick, a.diag.State
	a.diagMu.Unlock()
	loc := a.g.Exec.timeLocation()
	inWindow := withinWindowAt(plan.WindowStart, plan.WindowEnd, loc, time.Now())
	todaySent := 0
	for _, d := range a.cfg().Devices {
		todaySent += a.g.Exec.Metrics(d.UDID).SentOK
	}
	lastTask, lastRes := "", ""
	if tid, res, ok := a.lastTaskResult(); ok {
		lastTask, lastRes = tid, res
	}
	return autonomyStatus{
		Enabled: plan.Enabled, Running: a.running.Load(),
		LLMEnabled:     a.llm != nil && a.llm.Enabled(),
		State:          state,
		LastTask:       lastTask,
		LastTaskResult: lastRes,
		TodaySent:      todaySent,
		DailyCap:       plan.DailyCap,
		MaxFriends:     plan.MaxFriends,
		Window:         plan.WindowStart + "~" + plan.WindowEnd,
		InWindow:       inWindow,
		ReadyDevices:   ready,
		Reason:         reason,
		LastTick:       last,
	}
}

// withinWindowAt 与 executor.withinWindow 同规则，但时间可注入（决策预筛用）。
// 实际发送仍由 Executor 用真实时钟强制，两者规则一致、互不冲突。
func withinWindowAt(start, end string, loc *time.Location, at time.Time) bool {
	if start == "" && end == "" {
		return true
	}
	if loc == nil {
		loc = time.Local
	}
	hm := at.In(loc).Hour()*60 + at.In(loc).Minute()
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
