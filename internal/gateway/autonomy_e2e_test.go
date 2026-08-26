package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// e2eChatListXML 一个最小聊天列表页（含 2 个 1:1 好友，能解析出号码），供 mock WDA 返回。
const e2eChatListXML = `<?xml version="1.0" encoding="UTF-8"?>
<XCUIElementTypeApplication>
 <XCUIElementTypeTable name="ChatListView_TableView">
  <XCUIElementTypeCell name="+ 8 6,1 5 2,1 3 4 7,2 0 8 5" label="+ 8 6,1 5 2,1 3 4 7,2 0 8 5" value="你的消息, hello, 已发送到+ 8 6,1 5 2,1 3 4 7,2 0 8 5">
   <XCUIElementTypeStaticText name="WAChatSessionCell_Message" label="hello"/>
  </XCUIElementTypeCell>
  <XCUIElementTypeCell name="+ 8 6,1 7 6,8 8 5 4,0 7 7 5" label="+ 8 6,1 7 6,8 8 5 4,0 7 7 5" value="你的消息, 荔枝, 已发送到+ 8 6,1 7 6,8 8 5 4,0 7 7 5">
   <XCUIElementTypeStaticText name="WAChatSessionCell_Message" label="荔枝"/>
  </XCUIElementTypeCell>
 </XCUIElementTypeTable>
</XCUIElementTypeApplication>`

// fakeAutonomyWDA 是能跑完整发送链路的宽进宽出 WDA mock：
// 任意元素查找都返回同一个元素，/source 返回聊天列表，输入框/标题文本可配置。
type fakeAutonomyWDA struct {
	mu   sync.Mutex
	text string
	src  string
}

func (f *fakeAutonomyWDA) setText(s string) {
	f.mu.Lock()
	f.text = s
	f.mu.Unlock()
}

func (f *fakeAutonomyWDA) getText() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.text
}

func (f *fakeAutonomyWDA) handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	p := r.URL.Path
	switch {
	case p == "/status":
		fmt.Fprint(w, `{"value":{"ready":true,"os":{"version":"17.0"},"ios":{"ip":"127.0.0.1"}}}`)
	case p == "/session" && r.Method == http.MethodPost:
		_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]any{"sessionId": "s1"}})
	case strings.HasSuffix(p, "/source"):
		src := f.src
		if src == "" {
			src = e2eChatListXML
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"value": src})
	case strings.HasSuffix(p, "/elements") && r.Method == http.MethodPost:
		_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"ELEMENT": "E1"}}})
	case strings.HasSuffix(p, "/element") && r.Method == http.MethodPost:
		_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]any{"ELEMENT": "E1"}})
	case strings.Contains(p, "/element/") && strings.HasSuffix(p, "/text") && r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode(map[string]any{"value": f.getText()})
	case r.Method == http.MethodDelete && strings.HasPrefix(p, "/session/"):
		_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]any{}})
	case strings.HasSuffix(p, "/screenshot"):
		_ = json.NewEncoder(w).Encode(map[string]any{"value": ""})
	case r.Method == http.MethodPost && (strings.HasSuffix(p, "/wda/tap") || strings.HasSuffix(p, "/url")):
		_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]any{}})
	case r.Method == http.MethodPost:
		// click / value / clear / 其它命令
		_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]any{}})
	default:
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]any{"error": "no such element", "message": "not found"}})
	}
}

// TestAutonomyEndToEndSend 全链路闭环：自主回路 -> 提交 -> Executor 真正走 WDA 发送应用联系人
// -> 落库(results.db + sent_contacts) -> 状态回填(Status/lastTaskResult)。
// 不依赖真机：用 httptest mock WDA（宽进宽出）。这是"配置→运行→发送→结果可见"的可复现验证。
func TestAutonomyEndToEndSend(t *testing.T) {
	mock := &fakeAutonomyWDA{src: e2eChatListXML}
	srv := httptest.NewServer(http.HandlerFunc(mock.handler))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())

	cfg := &Config{
		// ActivateVia 留空（USB）：验证"USB 无隧道时也回退 ip:port 探活"——不再只认 USB 隧道。
		Devices: []Device{{UDID: "u1", IP: host, Port: port, LastHealth: map[string]any{"ok": true}}},
		Autonomy: AutonomyConfig{
			Enabled: true, Content: "您好 ${name}", MaxFriends: 30, DailyCap: 40,
		},
	}
	ex := NewExecutor(cfg, nil, nil, t.TempDir())
	gw := New(cfg, nil, ex, nil, nil)
	a := gw.Autonomy

	if err := a.step(context.Background()); err != nil {
		t.Fatalf("step error: %v", err)
	}

	// 等待执行器把 chat-list 明细落库（真正发送完成）。
	var taskID string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if tid, _, ok := a.lastTaskResult(); ok {
			taskID = tid
		}
		if tid := taskID; tid != "" {
			items := ex.readItems(tid)
			if rec, ok := items["chat-list"]; ok {
				if rec.Status == "sent" {
					break
				}
			}
		}
		if _, desc, ok := a.lastTaskResult(); ok && strings.HasPrefix(desc, "发送失败") {
			t.Fatalf("send failed early: %s", desc)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if taskID == "" {
		t.Fatal("no auto task was submitted")
	}

	items := ex.readItems(taskID)
	rec, ok := items["chat-list"]
	if !ok {
		t.Fatalf("chat-list item not persisted for task %s: %+v", taskID, items)
	}
	if rec.Status != "sent" || rec.ChatListSent != 2 {
		t.Fatalf("expected sent=2, got status=%q chat_list_sent=%d (err=%q)", rec.Status, rec.ChatListSent, rec.Error)
	}
	if rec.Content != "您好 ${name}" {
		t.Fatalf("content not preserved: %q", rec.Content)
	}

	// 结果回填到状态。
	_, desc, ok := a.lastTaskResult()
	if !ok || !strings.Contains(desc, "2") {
		t.Fatalf("lastTaskResult should say 2 contacts: %q", desc)
	}
	st := a.Status()
	if !strings.Contains(st.LastTaskResult, "2") {
		t.Fatalf("Status.LastTaskResult should include count: %+v", st)
	}
	if st.TodaySent != 1 {
		t.Fatalf("today_sent should be 1 (one chat-list item), got %d", st.TodaySent)
	}

	// sent_contacts 记录了 2 个联系人身份（跨天不重复的基础）。
	if got := ex.store.sentContactIdentities("u1", time.Now().Add(-24*time.Hour)); len(got) != 2 {
		t.Fatalf("expected 2 sent contact identities, got %v", got)
	}
}
