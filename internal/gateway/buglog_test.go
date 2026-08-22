package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteBugJSONL(t *testing.T) {
	dir := t.TempDir()
	e := NewExecutor(&Config{}, nil, nil, filepath.Join(dir, "results"))
	e.now = func() time.Time { return time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC) }
	e.writeBug(BugEvent{
		Time:   e.now().Format(time.RFC3339),
		TaskID: "t1", ItemID: "i1", Stage: "open_chat", Error: "unknown chat",
		LLM:    BugLLMInfo{Configured: true, Called: true, Model: "qwen-vl"},
		Screen: "unknown",
	})
	b, err := os.ReadFile(filepath.Join(dir, "bugs", "2026-08-22.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"task_id":"t1"`) || !strings.Contains(s, `"screen":"unknown"`) {
		t.Fatalf("bug log: %s", s)
	}
	if strings.Contains(s, "api_key") || strings.Contains(s, "sk-") {
		t.Fatal("bug log must not contain secrets")
	}
}
