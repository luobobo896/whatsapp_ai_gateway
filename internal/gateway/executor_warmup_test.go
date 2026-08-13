package gateway

import "testing"

// TestWarmupDailyCap 预热按天放量：起步 5、每日 +10、稳态 40 封顶。
func TestWarmupDailyCap(t *testing.T) {
	s := GatewaySchedule{WarmUpEnabled: true, WarmUpStartCap: 5, WarmUpDailyStep: 10, WarmUpSteadyCap: 40}
	cases := []struct {
		days int
		want int
	}{
		{1, 5}, {2, 15}, {3, 25}, {4, 35}, {5, 40}, {10, 40},
	}
	for _, c := range cases {
		if got := warmupDailyCap(s, c.days); got != c.want {
			t.Errorf("day %d cap = %d, want %d", c.days, got, c.want)
		}
	}
	// 未设置参数用默认值；days<1 归一为第 1 天。
	if got := warmupDailyCap(GatewaySchedule{WarmUpEnabled: true}, 0); got != 5 {
		t.Errorf("default day0 cap = %d, want 5", got)
	}
}

// TestNewSessionRatioExceeded 占比上限判定（百分比整数比较）。
func TestNewSessionRatioExceeded(t *testing.T) {
	cases := []struct {
		new, total, ratio int
		want              bool
	}{
		{0, 0, 30, false},     // 首条不计（分母为 0）
		{0, 3, 30, false},     // 1/4 = 25% ≤ 30%
		{3, 10, 40, false},    // 4/11 = 36% ≤ 40%
		{3, 10, 30, true},     // 4/11 = 36% > 30%
		{3, 9, 30, true},      // 4/10 = 40% > 30%
		{100, 1000, 0, false}, // 0=关闭
	}
	for i, c := range cases {
		if got := newSessionRatioExceeded(c.new, c.total, c.ratio); got != c.want {
			t.Errorf("case %d (%d/%d ratio %d) = %v, want %v", i, c.new, c.total, c.ratio, got, c.want)
		}
	}
}

// TestMetricsNewSessionCount 新会话计数并入今日统计并可跨天归档。
func TestMetricsNewSessionCount(t *testing.T) {
	dir := t.TempDir()
	e := mustExec(t, dir)
	e.now = dayAt(t, "2026-08-13")
	e.recordMetric("udid-a", "t1", "sent", true)
	e.recordMetric("udid-a", "t1", "sent", false)
	m := e.Metrics("udid-a")
	if m.SentOK != 2 || m.NewSessions != 1 || m.Total != 2 {
		t.Fatalf("today metrics = %+v", m)
	}
	// 跨天归档包含新会话计数。
	e.now = dayAt(t, "2026-08-14")
	e.recordMetric("udid-a", "t2", "sent", true)
	s := e.MetricsSummary()
	if s.Today.SentOK != 1 || len(s.History) != 1 {
		t.Fatalf("summary = %+v", s)
	}
	e2 := mustExec(t, dir)
	e2.now = dayAt(t, "2026-08-14")
	if got := e2.MetricsSummary().Today.NewSessions; got != 1 {
		t.Fatalf("persisted today new sessions = %d, want 1", got)
	}
}
