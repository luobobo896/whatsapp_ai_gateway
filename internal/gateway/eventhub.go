package gateway

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// EventHub 向所有已连接的管理页 WebSocket 广播任务事件（复用现有 coder/websocket 技术栈）。
type EventHub struct {
	mu    sync.Mutex
	conns map[*websocket.Conn]struct{}
}

func NewEventHub() *EventHub {
	return &EventHub{conns: map[*websocket.Conn]struct{}{}}
}

func (h *EventHub) register(c *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns[c] = struct{}{}
}

func (h *EventHub) unregister(c *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.conns, c)
}

// Publish 给当前所有管理页连接推一个事件（best-effort，慢/断开的连接被跳过）。
func (h *EventHub) Publish(typ string, payload any) {
	if h == nil {
		return
	}
	b, err := json.Marshal(map[string]any{
		"type":    typ,
		"payload": payload,
		"at":      time.Now().Format(time.RFC3339),
	})
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = c.Write(ctx, websocket.MessageText, b)
		cancel()
	}
}
