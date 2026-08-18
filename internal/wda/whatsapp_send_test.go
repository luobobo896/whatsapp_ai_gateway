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

// typedFake 扩展 fakeWDA：可注入 value 首次挂起、统计 clear/click/value 调用。
type typedFake struct {
	fakeWDA
	valueOK   atomic.Bool // false=首次 /value 挂起模拟过渡态超时
	valueDone atomic.Bool
	valueCnt  atomic.Int64
	clearCnt  atomic.Int64
	clickCnt  atomic.Int64
}

func (f *typedFake) handler(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/value") && r.Method == http.MethodPost:
		f.valueCnt.Add(1)
		if !f.valueDone.Load() && !f.valueOK.Load() {
			f.valueDone.Store(true)
			time.Sleep(1200 * time.Millisecond) // 挂起超过客户端超时 → Client.Timeout
			return
		}
		f.text.Store("你好") // 打入成功
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"value": nil})
	case strings.HasSuffix(r.URL.Path, "/clear") && r.Method == http.MethodPost:
		f.clearCnt.Add(1)
		f.text.Store("")
		w.WriteHeader(http.StatusOK)
	case strings.Contains(r.URL.Path, "/element/input-1/click") && r.Method == http.MethodPost:
		f.clickCnt.Add(1)
		w.WriteHeader(http.StatusOK)
	default:
		f.fakeWDA.handler(w, r)
	}
}

// TestEnsureTypedRetriesTransientTimeout 首次 type 挂起超时 → 重找元素重试成功。
func TestEnsureTypedRetriesTransientTimeout(t *testing.T) {
	f := &typedFake{}
	f.text.Store("") // 输入框为空
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	c := NewClient(srv.URL, 500*time.Millisecond) // 客户端超时 0.5s，1.2s 挂起必超时

	if err := ensureTyped(context.Background(), c, "s1", "你好", nil); err != nil {
		t.Fatalf("瞬时超时应自愈重试成功: %v", err)
	}
	if f.valueCnt.Load() != 2 {
		t.Fatalf("value 调用 = %d, want 2（首次超时+重试成功）", f.valueCnt.Load())
	}
	if f.clickCnt.Load() == 0 {
		t.Fatal("重试前应点按输入框拉起键盘")
	}
}

// TestEnsureTypedIdempotentSkip 输入框已有相同文本（上次已打入）→ 不再重复输入。
func TestEnsureTypedIdempotentSkip(t *testing.T) {
	f := &typedFake{}
	f.valueOK.Store(true)
	f.text.Store("你好") // 已有相同内容
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	c := NewClient(srv.URL, 2*time.Second)

	if err := ensureTyped(context.Background(), c, "s1", "你好", nil); err != nil {
		t.Fatalf("幂等跳过应成功: %v", err)
	}
	if f.valueCnt.Load() != 0 {
		t.Fatalf("已有相同文本不应再次 type（防重复），value = %d", f.valueCnt.Load())
	}
}

// TestEnsureTypedClearsDraft 残留草稿 → 先清空再打。
func TestEnsureTypedClearsDraft(t *testing.T) {
	f := &typedFake{}
	f.valueOK.Store(true)
	f.text.Store("旧草稿") // 残留
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	c := NewClient(srv.URL, 2*time.Second)

	if err := ensureTyped(context.Background(), c, "s1", "你好", nil); err != nil {
		t.Fatalf("清残留后应打入成功: %v", err)
	}
	if f.clearCnt.Load() != 1 {
		t.Fatalf("应清空一次残留草稿, clear = %d", f.clearCnt.Load())
	}
	if f.valueCnt.Load() != 1 || f.text.Load() != "你好" {
		t.Fatalf("应打入新内容, value=%d text=%v", f.valueCnt.Load(), f.text.Load())
	}
}
