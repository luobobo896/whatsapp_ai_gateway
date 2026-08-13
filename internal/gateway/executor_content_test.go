package gateway

import "testing"

// TestItemContentPreference 明细逐条内容优先于任务级内容（模板变量渲染），空则回退。
func TestItemContentPreference(t *testing.T) {
	task := TaskDispatch{Content: "任务级内容"}
	if got := itemContent(task, TaskItem{Content: "8613800000001 您好"}); got != "8613800000001 您好" {
		t.Fatalf("item content should win, got %q", got)
	}
	if got := itemContent(task, TaskItem{}); got != "任务级内容" {
		t.Fatalf("empty item content should fall back, got %q", got)
	}
}
