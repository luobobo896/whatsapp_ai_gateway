package gateway

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"wda-farm-gateway/internal/wda"
)

// BugEvent 发送失败时的排障记录（不含截图、不含 api_key）。
type BugEvent struct {
	Time   string     `json:"ts"`
	TaskID string     `json:"task_id"`
	ItemID string     `json:"item_id"`
	Phone  string     `json:"phone,omitempty"`
	Stage  string     `json:"stage"`
	Error  string     `json:"error"`
	Screen string     `json:"screen,omitempty"`
	Note   string     `json:"note,omitempty"`
	LLM    BugLLMInfo `json:"llm"`
}

type BugLLMInfo struct {
	Configured bool   `json:"configured"`
	Called     bool   `json:"called"`
	Model      string `json:"model,omitempty"`
	Err        string `json:"err,omitempty"`
}

func (e *Executor) recordBug(t TaskDispatch, it TaskItem, stage string, fail error, assist wda.SendAssist, client *wda.Client, sid string) {
	ev := BugEvent{
		Time:   e.now().Format(time.RFC3339),
		TaskID: t.TaskID,
		ItemID: it.ItemID,
		Phone:  it.Phone,
		Stage:  stage,
	}
	if fail != nil {
		ev.Error = fail.Error()
	}
	if c := e.llmClient(); c != nil && c.Enabled() {
		ev.LLM.Configured = true
		ev.LLM.Model = c.Model
	}
	// 不在失败路径同步调模型：欠费/超时不能再拖住主流程收口。
	_ = assist
	_ = client
	_ = sid
	e.writeBug(ev)
}

func (e *Executor) writeBug(ev BugEvent) {
	slog.Warn("send bug", "task", ev.TaskID, "item", ev.ItemID, "stage", ev.Stage,
		"screen", ev.Screen, "llm", ev.LLM.Configured, "called", ev.LLM.Called, "error", ev.Error)
	if e.resultsDir == "" {
		return
	}
	dir := filepath.Join(filepath.Dir(e.resultsDir), "bugs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Warn("bug log mkdir", "error", err)
		return
	}
	day := ev.Time
	if len(day) >= 10 {
		day = day[:10]
	} else {
		day = time.Now().Format("2006-01-02")
	}
	path := filepath.Join(dir, day+".jsonl")
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		slog.Warn("bug log open", "error", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		slog.Warn("bug log write", "error", err)
	}
}
