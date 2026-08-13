package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

var msgCounter int64

type envelope struct {
	V       int    `json:"v"`
	Type    string `json:"type"`
	MsgID   string `json:"msgId"`
	SentAt  string `json:"sentAt"`
	Payload any    `json:"payload"`
}

func newEnvelope(typ string, payload any) envelope {
	msgCounter++
	return envelope{
		V: 1, Type: typ,
		MsgID:   fmt.Sprintf("g:%d", msgCounter),
		SentAt:  time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		Payload: payload,
	}
}

// CloudLoop 云通道：凭证登录 -> hello -> 补报 -> device_list -> 心跳/报告泵 -> 收任务；断线退避重连。
func (g *Gateway) CloudLoop(ctx context.Context) {
	cfg := g.Cfg
	url := cfg.Cloud.WSURL
	token := cfg.Cloud.Token
	name := cfg.Cloud.GatewayName
	if name == "" {
		name, _ = os.Hostname()
	}
	if url == "" {
		slog.Info("cloud ws_url 未配置，跳过云连接")
		return
	}
	backoff := time.Second
	for ctx.Err() == nil {
		err := g.cloudSession(ctx, url, token, name)
		if err != nil {
			slog.Warn("cloud error", "error", err)
			g.SetConnected(false)
			g.SetLastError(err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

func (g *Gateway) cloudSession(ctx context.Context, url, token, name string) error {
	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		return err
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	g.SetConnected(true)
	slog.Info("cloud connected", "url", url, "gateway", name)

	write := func(typ string, payload any) error {
		return wsjson.Write(ctx, conn, newEnvelope(typ, payload))
	}
	if err := write("gateway:hello", map[string]string{"name": name, "version": "0.3.0"}); err != nil {
		return err
	}
	// 重连先补报本地结果，再上报设备清单（平台随后补推 pending 任务）。
	g.Exec.ResendPersisted()
	if err := write("device_list", map[string]any{"devices": g.deviceListReport()}); err != nil {
		return err
	}

	hb := time.Duration(g.Cfg.Cloud.HeartbeatInterval) * time.Second
	if hb <= 0 {
		hb = 20 * time.Second
	}

	// 报告/状态泵 + 心跳
	reportDone := make(chan struct{})
	statusDone := make(chan struct{})
	hbDone := make(chan struct{})
	go func() { defer close(reportDone); pump(ctx, conn, g.Exec.ReportQ, "item:result", write) }()
	go func() { defer close(statusDone); pump(ctx, conn, g.Exec.StatusQ, "device:status", write) }()
	go func() {
		defer close(hbDone)
		t := time.NewTicker(hb)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := write("gateway:heartbeat", map[string]any{}); err != nil {
					return
				}
			}
		}
	}()

	// 收帧
	for {
		var raw json.RawMessage
		var msg struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := wsjson.Read(ctx, conn, &raw); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "task:dispatch":
			var t TaskDispatch
			_ = json.Unmarshal(msg.Payload, &t)
			g.Exec.Submit(t)
		case "task:cancel":
			var p struct {
				TaskID string `json:"task_id"`
			}
			_ = json.Unmarshal(msg.Payload, &p)
			g.Exec.Cancel(p.TaskID)
		case "server:disconnect":
			slog.Warn("platform asked to disconnect")
			return nil
		case "easytier:config":
			if g.EasyTier != nil {
				if err := g.EasyTier.Apply(msg.Payload); err != nil {
					slog.Warn("easytier:config apply failed", "error", err)
				} else {
					slog.Info("easytier:config applied", "running", g.EasyTier.Running())
				}
			}
		}
	}
}

func pump[T any](ctx context.Context, conn *websocket.Conn, q <-chan T, typ string, write func(string, any) error) {
	for {
		select {
		case <-ctx.Done():
			return
		case v := <-q:
			if err := write(typ, v); err != nil {
				return
			}
		}
	}
}

func (g *Gateway) deviceListReport() []map[string]any {
	out := make([]map[string]any, 0, len(g.Cfg.Devices))
	for _, d := range g.Cfg.Devices {
		status := "offline"
		if h, ok := d.LastHealth["ok"].(bool); ok && h {
			status = "online"
		}
		if g.Exec.IsBusy(d.UDID) {
			status = "busy"
		}
		out = append(out, map[string]any{
			"udid": d.UDID, "name": d.Name, "model": d.Model,
			"ios_version": d.IOSVersion, "wda_ip": d.IP, "wda_port": d.Port,
			"wda_status": status, "whatsapp_version": "",
		})
	}
	return out
}
