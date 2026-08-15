package wda

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeWDA 模拟 WDA 元素查找与文本读取（按 selector 前缀分发，可注入动态文本）。
type fakeWDA struct {
	text atomic.Value // string：输入框/标题当前文本
}

func (f *fakeWDA) handler(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/element") && r.Method == http.MethodPost:
		var body struct {
			Using string `json:"using"`
			Value string `json:"value"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		isInput := strings.Contains(body.Value, "TextView")
		isTitle := strings.Contains(body.Value, "StaticText") || strings.Contains(body.Value, "ChatTitleView")
		if isInput || isTitle {
			id := "input-1"
			if isTitle {
				id = "title-1"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]any{"ELEMENT": id}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]any{
			"error": "no such element", "message": "unable to find an element"}})
	case strings.Contains(r.URL.Path, "/element/") && strings.HasSuffix(r.URL.Path, "/text"):
		_ = json.NewEncoder(w).Encode(map[string]any{"value": f.text.Load().(string)})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// TestConfirmSentCleared 发送后输入框被清空 → 确认通过。
func TestConfirmSentCleared(t *testing.T) {
	f := &fakeWDA{}
	f.text.Store("你好世界")
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	c := NewClient(srv.URL, 5*time.Second)

	// 首次读到原文（发送尚未生效），随后输入框清空。
	go func() {
		time.Sleep(300 * time.Millisecond)
		f.text.Store("")
	}()
	if err := confirmSent(context.Background(), c, "s1", "你好世界"); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

// TestConfirmSentResidual 发送后输入框内容一直残留 → 返回 send unconfirmed 错误。
func TestConfirmSentResidual(t *testing.T) {
	f := &fakeWDA{}
	f.text.Store("你好世界")
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	c := NewClient(srv.URL, 15*time.Second)

	err := confirmSent(context.Background(), c, "s1", "你好世界")
	if err == nil || !strings.Contains(err.Error(), "send unconfirmed") {
		t.Fatalf("want send unconfirmed error, got %v", err)
	}
}

// TestChatOpenedForTitleMatch 标题含号码时必须与目标一致；无号码（联系人名）视为打开。
func TestChatOpenedForTitleMatch(t *testing.T) {
	mk := func(title string) (*fakeWDA, *Client) {
		f := &fakeWDA{}
		f.text.Store(title)
		srv := httptest.NewServer(http.HandlerFunc(f.handler))
		return f, NewClient(srv.URL, 5*time.Second)
	}
	// 标题是格式化号码（+86 152 1347 2085），与目标 digits 匹配 → true
	_, c := mk("+86 152 1347 2085")
	if !chatOpenedFor(context.Background(), c, "s1", "8615213472085", time.Second) {
		t.Fatal("title digits match should open")
	}
	// 标题是别的号码（停留在上一会话）→ false，必须走兜底
	_, c = mk("+86 138 0000 0001")
	if chatOpenedFor(context.Background(), c, "s1", "8615213472085", time.Second) {
		t.Fatal("title digits mismatch should not open")
	}
	// 标题是联系人姓名（无号码可比）→ 输入框出现即 true
	_, c = mk("张三")
	if !chatOpenedFor(context.Background(), c, "s1", "8615213472085", time.Second) {
		t.Fatal("contact-name title should open")
	}
}
