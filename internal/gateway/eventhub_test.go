package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestEventHubBroadcastOverWS：管理页 WebSocket 连接能实时收到任务事件（复用 coder/websocket）。
func TestEventHubBroadcastOverWS(t *testing.T) {
	hub := NewEventHub()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		hub.register(c)
		defer hub.unregister(c)
		for {
			if _, _, err := c.Read(context.Background()); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	time.Sleep(150 * time.Millisecond) // 等 server 完成 handshake 后的 register
	hub.Publish("task:new", map[string]any{"task_id": "t1", "udid": "u1", "item_count": 3})

	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, data, err := c.Read(cctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "task:new") || !strings.Contains(s, "t1") || !strings.Contains(s, `"item_count":3`) {
		t.Fatalf("expected task:new event with payload, got %s", s)
	}
}
