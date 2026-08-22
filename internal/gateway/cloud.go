package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

var msgCounter atomic.Int64

// dialStatusRe 匹配拨号错误尾部 "…but got 502" 里的 HTTP 状态码。
var dialStatusRe = regexp.MustCompile(`but got (\d{3})$`)

// dialStatus 从 "failed to WebSocket dial: … but got 502" 中提取 HTTP 状态码。
func dialStatus(err error) (int, bool) {
	m := dialStatusRe.FindStringSubmatch(err.Error())
	if len(m) != 2 {
		return 0, false
	}
	code, e := strconv.Atoi(m[1])
	return code, e == nil
}

type envelope struct {
	V       int    `json:"v"`
	Type    string `json:"type"`
	MsgID   string `json:"msgId"`
	SentAt  string `json:"sentAt"`
	Payload any    `json:"payload"`
}

func newEnvelope(typ string, payload any) envelope {
	return envelope{
		V: 1, Type: typ,
		MsgID:   fmt.Sprintf("g:%d", msgCounter.Add(1)),
		SentAt:  time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		Payload: payload,
	}
}

// describeCloudError 把底层错误转成可读说明：
// EOF（无关闭帧）= 远端直接把 TCP 断开；关闭码 = 平台主动关闭（4005 为凭证被吊销）；
// 拨号握手非 101 = 平台暂不可用（502/503/504）或凭证被拒（401/403）。
func describeCloudError(err error) string {
	if code, ok := dialStatus(err); ok {
		switch code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return "平台拒绝连接（HTTP 401/403）：网关凭证无效或已被吊销，请到平台「组网」页重新签发"
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return fmt.Sprintf("平台服务暂不可用（HTTP %d）：平台正在重启/部署，网关会自动重试恢复", code)
		default:
			return fmt.Sprintf("WebSocket 握手失败（HTTP %d）：平台暂不可用或网络异常，网关会自动重试", code)
		}
	}
	if code := websocket.CloseStatus(err); code != -1 {
		switch code {
		case websocket.StatusNormalClosure:
			return "平台主动关闭连接（正常关闭）"
		case 4005:
			return "平台已吊销该网关凭证（关闭码 4005），请到平台「组网」页重新签发"
		default:
			return fmt.Sprintf("平台主动关闭连接（关闭码 %d）", code)
		}
	}
	if errors.Is(err, io.EOF) {
		return "连接被远端断开（TCP EOF，未收到关闭帧）：通常是平台重启或网络抖动，已自动重连"
	}
	return err.Error()
}

// CloudLoop 云通道：凭证登录 -> hello -> 补报 -> device_list -> 心跳/报告泵 -> 收任务；断线退避重连。
func (g *Gateway) CloudLoop(ctx context.Context) {
	if !g.Cfg.Cloud.Enabled || g.Cfg.Cloud.WSURL == "" {
		slog.Info("云通道未启用（enabled=false 或 ws_url 未配置），跳过云连接")
		return
	}
	backoff := time.Second
	revoked := false
	for ctx.Err() == nil {
		// 每次会话重连都读取最新配置，登录自动签发凭证后无需重启进程即可生效。
		url := g.Cfg.Cloud.WSURL
		token := g.Cfg.Cloud.Token
		name := g.Cfg.Cloud.GatewayName
		if name == "" {
			// 与注册（provisionGatewayToken）同策略去域后缀：平台校验 hello 帧的
			// 网关名必须与凭证注册名一致，裸 hostname 带 .local 会被 4004 踢掉。
			if host, err := os.Hostname(); err == nil {
				name = strings.SplitN(host, ".", 2)[0]
			}
		}
		// 凭证未签发（全新安装未登录）不空拨：避免 4001 反复被拒，在首次登录/
		// 选租户过程中弹出误导性的"云通道错误"弹窗（也消除服务端无效凭证日志噪音）。
		// 登录自动签发后经 cloudReconnect 立即拨号；此处仅低频轮询兜底。
		if token == "" {
			select {
			case <-ctx.Done():
				return
			case <-g.cloudReconnect:
			case <-time.After(5 * time.Second):
			}
			continue
		}
		start := time.Now()
		err := g.cloudSessionSafe(ctx, url, token, name)
		g.SetConnected(false)
		if ctx.Err() != nil {
			return
		}
		switch {
		case err == nil:
			// server:disconnect 等干净断开：短退避。
			backoff = time.Second
		default:
			text := describeCloudError(err)
			slog.Warn("cloud session ended", "error", text,
				"duration", time.Since(start).Round(time.Second).String())
			// 只有凭证类错误需要人工干预；瞬时错误（平台重启/网络抖动）网关会自动重试恢复。
			actionable := websocket.CloseStatus(err) == 4005 || websocket.CloseStatus(err) == 4001
			if code, ok := dialStatus(err); ok && (code == http.StatusUnauthorized || code == http.StatusForbidden) {
				actionable = true
			}
			g.SetLastError(text, actionable)
			if websocket.CloseStatus(err) == 4005 {
				revoked = true
			}
			if code, ok := dialStatus(err); ok && (code == http.StatusUnauthorized || code == http.StatusForbidden) {
				revoked = true
			}
			switch {
			case revoked:
				backoff = 60 * time.Second // 凭证吊销，重连也会被拒，慢点试
			case time.Since(start) > 60*time.Second:
				backoff = time.Second // 稳定会话断开：快速重连，不累积退避
			default:
				backoff *= 2
				if backoff > 30*time.Second {
					backoff = 30 * time.Second
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-g.cloudReconnect:
			backoff = time.Second // 凭证轮换等主动触发：立即重连
		case <-time.After(backoff):
		}
	}
}

// cloudSessionSafe 带恐慌防护的云会话：会话内 panic 不拖死重连循环。
func (g *Gateway) cloudSessionSafe(ctx context.Context, url, token, name string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("cloud session panicked: %v", r)
		}
	}()
	return g.cloudSession(ctx, url, token, name)
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
	g.setCloudConn(conn)
	defer func() {
		g.clearCloudConn(conn)
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}()
	g.SetConnected(true)
	g.SetLastError("", false) // 连接成功即清掉上一次的旧错误，避免 UI 一直展示历史告警
	slog.Info("cloud connected", "url", url, "gateway", name)

	// 会话级上下文：会话结束时取消，让写入泵及时退出，避免向已关闭连接写入。
	sessCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// 所有写帧共用一把锁与写超时：避免并发写交叉，也避免半死连接上写卡死。
	var writeMu sync.Mutex
	write := func(typ string, payload any) error {
		wctx, wcancel := context.WithTimeout(sessCtx, 10*time.Second)
		defer wcancel()
		writeMu.Lock()
		defer writeMu.Unlock()
		return wsjson.Write(wctx, conn, newEnvelope(typ, payload))
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

	// 报告/状态/任务汇总泵 + 心跳
	reportDone := make(chan struct{})
	statusDone := make(chan struct{})
	summaryDone := make(chan struct{})
	hbDone := make(chan struct{})
	go func() { defer close(reportDone); pump(sessCtx, conn, g.Exec.ReportQ, "item:result", write) }()
	go func() { defer close(statusDone); pump(sessCtx, conn, g.Exec.StatusQ, "device:status", write) }()
	go func() { defer close(summaryDone); pump(sessCtx, conn, g.Exec.SummaryQ, "task:summary", write) }()
	go func() {
		defer close(hbDone)
		t := time.NewTicker(hb)
		defer t.Stop()
		for {
			select {
			case <-sessCtx.Done():
				return
			case <-t.C:
				// 协议层 ping 探活（对方 WS 库自动回 pong），再发应用层心跳。
				pctx, pcancel := context.WithTimeout(sessCtx, 5*time.Second)
				err := conn.Ping(pctx)
				pcancel()
				if err != nil {
					return
				}
				if err := write("gateway:heartbeat", map[string]any{}); err != nil {
					return
				}
			}
		}
	}()

	// 收帧
	var readErr error
readLoop:
	for {
		var raw json.RawMessage
		var msg struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := wsjson.Read(sessCtx, conn, &raw); err != nil {
			readErr = err
			break
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
		case "server:ack":
			var ack struct {
				TenantID   string `json:"tenantId"`
				TenantName string `json:"tenantName"`
				UserEmail  string `json:"userEmail"`
				UserName   string `json:"userName"`
			}
			if json.Unmarshal(msg.Payload, &ack) == nil && ack.TenantID != "" {
				g.SetIdentity(ack.TenantID, ack.TenantName, ack.UserEmail, ack.UserName)
			}
		case "server:disconnect":
			slog.Warn("platform asked to disconnect")
			readErr = nil
			break readLoop
		case "easytier:config":
			// 平台「组网」页下发：保存配置并自动启动/重启（无人值守，不弹授权框）。
			if g.EasyTier != nil {
				if err := g.EasyTier.Apply(msg.Payload); err != nil {
					slog.Warn("easytier:config apply failed", "error", err)
				} else {
					slog.Info("easytier:config applied", "running", g.EasyTier.Running())
				}
			}
		case "model:config":
			var cfg LLMConfig
			if err := json.Unmarshal(msg.Payload, &cfg); err != nil {
				slog.Warn("model:config parse failed", "error", err)
			} else if err := g.ApplyLLMConfig(cfg); err != nil {
				slog.Warn("model:config apply failed", "error", err)
			} else {
				slog.Info("model:config applied", "model", cfg.Model, "enabled", cfg.BaseURL != "" && cfg.Model != "")
			}
		}
	}

	// 会话结束：先取消写入泵并等它们全部退出，再关闭连接。
	cancel()
	<-reportDone
	<-statusDone
	<-summaryDone
	<-hbDone
	return readErr
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
	// 与 Web 设备列表一致：已配置设备始终上报（USB 直连 / Wi-Fi 在线 / 离线），
	// 离线状态由 wda_status 表达；拔掉 USB 后设备不消失。
	usb := Discover()
	usbSet := map[string]bool{}
	for _, d := range usb {
		usbSet[d.UDID] = true
	}
	out := make([]map[string]any, 0, len(g.Cfg.Devices))
	for i := range g.Cfg.Devices {
		d := &g.Cfg.Devices[i]
		if d.UDID == "" || g.Cfg.IsIgnored(d.UDID) {
			continue
		}
		attached := attachedUSB(d.UDID, usbSet, TunnelAddr(d.UDID) != "")
		conn := "wifi"
		if attached {
			conn = "usb"
		}
		out = append(out, map[string]any{
			"udid": d.UDID, "serial": g.SerialOf(d.UDID), "name": d.Name, "model": d.Model,
			"ios_version": d.IOSVersion, "wda_ip": d.IP, "wda_port": d.Port,
			"conn_type": conn, "wda_status": g.deviceCloudStatus(d, attached), "whatsapp_version": "",
		})
	}
	return out
}

// deviceCloudStatus 返回与设备列表 UI 一致的云上行状态：
// busy > 机上 /status 通 > 主机激活进程仍在（USB/宽限期）> offline。
func (g *Gateway) deviceCloudStatus(d *Device, usb bool) string {
	attached := usb || TunnelAddr(d.UDID) != ""
	host := g.WDA.Running(d.UDID)
	inGrace := host && g.WDA.StartedSecondsAgo(d.UDID) < wdaStartGrace.Seconds()
	return wdaCloudStatus(g.Exec.IsBusy(d.UDID), host, healthOK(d.LastHealth), attached, inGrace)
}

// attachedUSB：idevice 枚举到，或 USB 隧道仍在。枚举空但 iproxy 还在时不能报 Wi-Fi/离线。
func attachedUSB(udid string, usbSet map[string]bool, tunnelUp bool) bool {
	if usbSet[udid] {
		return true
	}
	return tunnelUp
}

// wdaCloudStatus：busy > 机上 WDA HTTP 通 > 主机激活进程仍在（USB/宽限期）> offline。
// 激活进程会因拔 USB 退出（xcodebuild exit 75）；XCTest 仍在手机 :8100 时必须保持 online。
func wdaCloudStatus(busy, running, healthy, attached, inGrace bool) string {
	if busy {
		return "busy"
	}
	if healthy {
		return "online"
	}
	if running && (attached || inGrace) {
		return "online"
	}
	return "offline"
}

// wdaAppearsRunning 本地列表的「WDA 在跑」：主机进程或机上 /status 通。
func wdaAppearsRunning(hostProc bool, lastHealth map[string]any) bool {
	return hostProc || healthOK(lastHealth)
}

// rememberCloudStatus 记录上次上报的云状态。
func (g *Gateway) rememberCloudStatus(udid, status string) {
	g.statusMu.Lock()
	g.lastStatus[udid] = status
	g.statusMu.Unlock()
}

// reportCloudStatusIfChanged 非忙碌设备云状态变化时上报 device:status。
func (g *Gateway) reportCloudStatusIfChanged(d *Device, usb bool, errMsg string) {
	status := g.deviceCloudStatus(d, usb)
	conn := "wifi"
	if usb {
		conn = "usb"
	}
	g.statusMu.Lock()
	prev := g.lastStatus[d.UDID]
	changed := prev != status
	if changed {
		g.lastStatus[d.UDID] = status
	}
	g.statusMu.Unlock()
	if changed {
		g.Exec.status(DeviceStatus{UDID: d.UDID, WDAStatus: status, Error: errMsg, ConnType: conn})
	}
}
