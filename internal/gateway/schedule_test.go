package gateway

import (
	"testing"
	"time"
)

func TestParseHM(t *testing.T) {
	if got := parseHM("09:00"); got != 9*60 {
		t.Fatalf("parseHM(09:00)=%d", got)
	}
	if got := parseHM("21:30"); got != 21*60+30 {
		t.Fatalf("parseHM(21:30)=%d", got)
	}
}

func TestNextIntervalJitter(t *testing.T) {
	// 固定间隔：jitter=0 时等于 base。
	if got := nextInterval(GatewaySchedule{}, 20); got != 20*time.Second {
		t.Fatalf("fixed interval = %v", got)
	}
	// 抖动区间：多次取样都落在 [base-jitter, base+jitter]，且下限 1s。
	s := GatewaySchedule{IntervalJitterSec: 5}
	for i := 0; i < 200; i++ {
		d := nextInterval(s, 10)
		if d < 5*time.Second || d > 15*time.Second {
			t.Fatalf("jitter interval out of range: %v", d)
		}
	}
	if got := nextInterval(GatewaySchedule{IntervalJitterSec: 30}, 1); got < 1*time.Second {
		t.Fatalf("interval should clamp to >=1s: %v", got)
	}
}

func TestWithinWindow(t *testing.T) {
	now := time.Now()
	cur := now.Format("15:04")
	// 包含当前时间的窗口。
	if !withinWindow("00:00", "23:59", time.Local) {
		t.Fatal("00:00-23:59 should be within window")
	}
	_ = cur
	// 空窗口 = 不限制。
	if !withinWindow("", "", time.Local) {
		t.Fatal("empty window should always allow")
	}
	// 仅 start：当前时间 >= start 才允许。用过去时间点应通过。
	if !withinWindow("00:00", "", time.Local) {
		t.Fatal("00:00 start should allow")
	}
	// 仅 end：当前时间 < end 才允许。
	if withinWindow("", "00:00", time.Local) {
		t.Fatal("end=00:00 should deny (unless midnight)")
	}
}
