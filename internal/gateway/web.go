package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// Handler 返回网关 HTTP 路由（REST + 静态页）。会话库打不开时返回错误。
func (g *Gateway) Handler(staticDir string) (http.Handler, error) {
	auth, err := NewWebAuth(g.Cfg, filepath.Join(g.Cfg.Dir(), "data", "sessions.db"))
	if err != nil {
		return nil, err
	}
	auth.onToken = g.ApplyCloudToken
	mux := http.NewServeMux()

	// 登录/登出/会话状态（公开）。
	mux.HandleFunc("/api/login", auth.HandleLogin)
	mux.HandleFunc("/api/logout", auth.HandleLogout)
	mux.HandleFunc("/api/session", auth.HandleSession)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			// 管理页单页资源：禁止缓存，保证前端更新后用户普通刷新（Cmd+R）即生效，
			// 否则浏览器启发式缓存旧 JS 导致新功能（删除即隐藏等）不生效。
			// 注意：不能用 http.ServeFile——Go 的 fixPragmaCacheControl 会在写响应头时
			// 按 Pragma/Cache-Control 组合改写/丢弃头，实测 Cache-Control 不生效；
			// 改为读文件 + 显式写头，完全控制缓存语义。
			data, err := os.ReadFile(filepath.Join(staticDir, "index.html"))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
			_, _ = w.Write(data)
			return
		}
		http.NotFound(w, r)
	})

	// /debug/pprof 生产观测端点：仅允许本机回环访问（goroutine/内存泄漏排查）。
	mux.HandleFunc("/debug/pprof/", func(w http.ResponseWriter, r *http.Request) {
		if !loopbackRequest(r) {
			http.NotFound(w, r)
			return
		}
		switch strings.TrimPrefix(r.URL.Path, "/debug/pprof/") {
		case "cmdline":
			pprof.Cmdline(w, r)
		case "profile":
			pprof.Profile(w, r)
		case "symbol":
			pprof.Symbol(w, r)
		case "trace":
			pprof.Trace(w, r)
		default:
			pprof.Index(w, r)
		}
	})

	mux.HandleFunc("/api/cloud", func(w http.ResponseWriter, r *http.Request) {
		tid, tname, uemail, uname := g.Identity()
		writeJSON(w, map[string]any{
			"connected": g.Connected(), "connected_at": g.ConnectedAt(),
			"ws_url": g.Cfg.Cloud.WSURL, "gateway_name": g.Cfg.Cloud.GatewayName,
			"token_configured": g.Cfg.Cloud.Token != "", "last_error": g.LastError(),
			"last_error_actionable": g.LastErrorActionable(),
			"tenant_id":             tid, "tenant_name": tname, "user_email": uemail, "user_name": uname,
			"executor": g.Exec.Status(),
		})
	})

	// /api/cloud/config 云通道连接设置：GET 脱敏读取，PUT 更新并热生效（连接立即重建）。
	mux.HandleFunc("/api/cloud/config", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, map[string]any{
				"ws_url": g.Cfg.Cloud.WSURL, "gateway_name": g.Cfg.Cloud.GatewayName,
				"enabled": g.Cfg.Cloud.Enabled, "token_configured": g.Cfg.Cloud.Token != "",
			})
		case http.MethodPut:
			var body struct {
				WSURL       string `json:"ws_url"`
				GatewayName string `json:"gateway_name"`
				Enabled     bool   `json:"enabled"`
				Token       string `json:"token"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "请求体格式错误"})
				return
			}
			if body.WSURL == "" {
				body.WSURL = g.Cfg.Cloud.WSURL // 留空保持现值
			}
			normalized := normalizeCloudWSURL(body.WSURL) != body.WSURL
			if body.Enabled && normalizeCloudWSURL(body.WSURL) == "" {
				writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "启用云通道必须填写平台地址"})
				return
			}
			if err := g.Cfg.SetCloud(body.WSURL, body.GatewayName, body.Enabled, body.Token); err != nil {
				writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			g.EnsureCloudLoop() // 冷启动未启用后首次配置：热拉起云循环
			slog.Info("cloud config updated", "ws_url", g.Cfg.Cloud.WSURL, "enabled", body.Enabled)
			writeJSON(w, map[string]any{
				"ok": true, "enabled": g.Cfg.Cloud.Enabled, "ws_url": g.Cfg.Cloud.WSURL,
				"normalized": normalized, "token_configured": g.Cfg.Cloud.Token != "",
			})
		default:
			writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	})

	mux.HandleFunc("/api/devices", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		writeJSON(w, g.deviceList())
	})

	// /api/llm 当前视觉/LLM 模型配置（平台下发，api_key 不回传明文）。
	mux.HandleFunc("/api/llm", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		c := g.Cfg.LLM
		writeJSON(w, map[string]any{
			"enabled":  c.BaseURL != "" && c.Model != "",
			"base_url": c.BaseURL, "model": c.Model, "has_api_key": c.APIKey != "",
		})
	})

	// /api/autonomy 自主群发配置：GET 脱敏读取（无密钥），PUT 更新并持久化（热生效）。
	mux.HandleFunc("/api/autonomy", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			c := g.Cfg.Autonomy
			writeJSON(w, map[string]any{
				"enabled": c.Enabled, "content": c.Content, "max_friends": c.MaxFriends,
				"window_start": c.WindowStart, "window_end": c.WindowEnd,
				"interval_sec": c.IntervalSec, "burst_count": c.BurstCount, "burst_pause_sec": c.BurstPauseSec,
				"daily_cap": c.DailyCap, "max_new_session_ratio": c.MaxNewSessionRatio, "tick_interval": c.TickInterval,
				"chat_list_repeat_days": g.Cfg.Web.ChatListRepeatDays,
			})
		case http.MethodPut:
			var body struct {
				AutonomyConfig
				ChatListRepeatDays int `json:"chat_list_repeat_days"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "请求体格式错误"})
				return
			}
			if body.Enabled && strings.TrimSpace(body.Content) == "" {
				writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "启用前必须填写群发话术 content"})
				return
			}
			if err := g.Cfg.SetAutonomy(body.AutonomyConfig); err != nil {
				writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			if body.ChatListRepeatDays > 0 {
				if err := g.Cfg.SetChatListRepeatDays(body.ChatListRepeatDays); err != nil {
					writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
					return
				}
			}
			slog.Info("autonomy config updated", "enabled", body.Enabled, "content_len", len(body.Content))
			writeJSON(w, map[string]any{"ok": true, "enabled": g.Cfg.Autonomy.Enabled})
		default:
			writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	})

	// /api/autonomy/status 自主群发状态与“为何未发”诊断。
	mux.HandleFunc("/api/autonomy/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		writeJSON(w, g.Autonomy.Status())
	})

	// /api/wda/status 网关当前 WDA 控制器包（版本/签名模式/校验和；供管理页显示"已更新"）。
	mux.HandleFunc("/api/wda/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		writeJSON(w, g.WdaStatus())
	})

	// /api/ws 管理页实时任务事件通道（复用现有 coder/websocket；受同一会话鉴权保护）。
	mux.HandleFunc("/api/ws", func(w http.ResponseWriter, r *http.Request) {
		if g.Hub == nil {
			writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "event hub unavailable"})
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		g.Hub.register(c)
		defer func() {
			g.Hub.unregister(c)
			_ = c.Close(websocket.StatusNormalClosure, "")
		}()
		// 读循环只为感知断开（管理页不在该连接上发业务消息）。
		for {
			if _, _, err := c.Read(r.Context()); err != nil {
				return
			}
		}
	})

	// /api/metrics 网关级发送统计聚合（今日汇总 + 分设备 + 历史按天，落盘持久化）。
	mux.HandleFunc("/api/metrics", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		writeJSON(w, g.Exec.MetricsSummary())
	})

	// /api/tasks 本地已持久化任务列表（发送明细核对入口）。
	mux.HandleFunc("/api/tasks", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		writeJSON(w, map[string]any{"tasks": g.Exec.TaskList()})
	})

	// /api/tasks/{task_id} 单任务发送明细（offset/limit 分页，默认 limit=500）。
	mux.HandleFunc("/api/tasks/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		taskID := strings.TrimPrefix(r.URL.Path, "/api/tasks/")
		if taskID == "" || strings.Contains(taskID, "/") {
			http.NotFound(w, r)
			return
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		items, total := g.Exec.TaskDetail(taskID, offset, limit)
		writeJSON(w, map[string]any{
			"task_id": taskID, "total": total, "offset": offset,
			"summary": g.Exec.readSummary(taskID), "items": items,
		})
	})

	// /api/items 按设备分组的发送明细（跨任务视图；升级前缺设备上下文的历史记录尽力归因）。
	mux.HandleFunc("/api/items", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		groups, truncated := g.Exec.DeviceItems(r.URL.Query().Get("udid"), limit)
		if groups == nil {
			groups = []DeviceItemGroup{}
		}
		writeJSON(w, map[string]any{"groups": groups, "truncated": truncated})
	})

	mux.HandleFunc("/api/easytier/status", func(w http.ResponseWriter, r *http.Request) {
		if g.EasyTier == nil {
			writeJSON(w, map[string]any{"running": false, "configured": false, "peers": []any{}, "error": "", "node": nil})
			return
		}
		writeJSON(w, g.EasyTier.Status())
	})
	mux.HandleFunc("/api/easytier/config", func(w http.ResponseWriter, r *http.Request) {
		if g.EasyTier == nil {
			writeJSON(w, map[string]any{})
			return
		}
		writeJSON(w, g.EasyTier.PublicConfig())
	})
	mux.HandleFunc("/api/easytier/action", func(w http.ResponseWriter, r *http.Request) {
		if g.EasyTier == nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"detail": "easytier 未配置"})
			return
		}
		var body struct {
			Action string `json:"action"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var ok bool
		var err error
		switch body.Action {
		case "start":
			ok, err = g.EasyTier.Start(true)
		case "stop":
			ok = g.EasyTier.Stop()
		case "restart":
			ok, err = g.EasyTier.Restart(true)
		default:
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"detail": "action 必须为 start/stop/restart"})
			return
		}
		if err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"detail": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": ok, "running": g.EasyTier.Running()})
	})

	// /api/usbmux-net usbmux 无线保活：GET 状态+配置，PUT 改开关，POST 立即修复。
	mux.HandleFunc("/api/usbmux-net", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, g.usbmuxNetStatus())
		case http.MethodPut:
			var body struct {
				AutoRepair bool `json:"auto_repair"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "请求体格式错误"})
				return
			}
			if err := g.Cfg.SetUsbmuxNet(body.AutoRepair); err != nil {
				writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			slog.Info("usbmux-net config updated", "auto_repair", body.AutoRepair)
			writeJSON(w, g.usbmuxNetStatus())
		case http.MethodPost:
			ctx, cancel := context.WithTimeout(context.Background(), usbmuxNetVerifyTimeout+5*time.Second)
			defer cancel()
			if err := g.usbmuxNetRepair(ctx); err != nil {
				writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, map[string]any{"ok": true, "status": g.usbmuxNetStatus()})
		default:
			writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	})

	mux.HandleFunc("/api/devices/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/devices/")
		parts := strings.SplitN(rest, "/", 2)
		udid := parts[0]
		if udid == "" {
			http.NotFound(w, r)
			return
		}
		if len(parts) == 1 {
			http.NotFound(w, r)
			return
		}
		action := parts[1]
		switch action {
		case "activate":
			if r.Method != http.MethodPost {
				writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
				return
			}
			port, _ := strconv.Atoi(r.URL.Query().Get("port"))
			if port == 0 {
				port = 8100
			}
			var body struct {
				Via      string `json:"via"`
				Passcode string `json:"passcode"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			via := parseActivateVia(r.URL.Query().Get("via"))
			if strings.TrimSpace(body.Via) != "" {
				via = parseActivateVia(body.Via)
			}
			// Network 激活前必须在页面输入统一锁屏密码 0000（业务规则校验）。
			if via == activateViaNetwork {
				if err := requirePasscode0000(body.Passcode); err != nil {
					writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
					return
				}
			}
			dev := g.Cfg.Device(udid)
			prevVia := activateViaUSB
			if dev != nil {
				prevVia = parseActivateVia(dev.ActivateVia)
			}
			if dev == nil {
				dev = &Device{UDID: udid, Port: port, AutoReactivate: true, ActivateVia: via}
				g.Cfg.Devices = append(g.Cfg.Devices, *dev)
				dev = g.Cfg.Device(udid)
				_ = g.Cfg.Save()
			} else {
				dev.AutoReactivate = true
				dev.ActivateVia = via
				_ = g.Cfg.Save()
			}
			// 同一通道且 /status 已通：不必重拉。换通道必须先停再启，USB 会话会拆掉 Network。
			sameVia := prevVia == via
			if sameVia && (dev.IP != "" || TunnelAddr(udid) != "") {
				if h := g.checkWDA(dev); h.OK {
					applyHealth(dev, h)
					if h.IP != "" {
						_ = syncStoredWifiIP(dev, h.IP)
					}
					_ = g.Cfg.Save()
					writeJSON(w, map[string]any{"udid": udid, "status": "activated", "ready": true, "auto_reactivate": true, "via": via, "activate_via": via})
					return
				}
			}
			if g.WDA.Running(udid) {
				g.WDA.Stop(udid)
			}
			if via == activateViaNetwork && !wifiAuthorized(udid, dev.WifiDebug) {
				writeJSONStatus(w, http.StatusBadRequest, map[string]any{
					"error": errNeedWifiAuth.Error(), "need_wifi_auth": true,
				})
				return
			}
			dropTunnel(udid)
			EnsureUSBTunnels(map[string]int{udid: port}, map[string]string{normalizeUDID(udid): via})
			// 手动激活允许清除崩溃冷却（人工处理签名/信任问题后立即重试）。
			g.WDA.ResetCrashCooldown(udid)
			if err := g.WDA.Activate(udid, port, udid, via); err != nil {
				body := map[string]any{"error": "激活失败：" + err.Error()}
				if isDevicePasscodeErr(err) {
					body["need_passcode"] = true
				}
				writeJSONStatus(w, http.StatusBadRequest, body)
				return
			}
			res := g.waitWDAReady(udid, port, 70*time.Second, via)
			ready, _ := res["ready"].(bool)
			if ready {
				g.tapAgentPermissions(udid, port)
			}
			g.KickWatchdog()
			writeJSON(w, res)
		case "authorize-wifi":
			if r.Method != http.MethodPost {
				writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
				return
			}
			var body struct {
				Passcode string `json:"passcode"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if err := requirePasscode0000(body.Passcode); err != nil {
				writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			if !usbPresent(udid) {
				writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": errWifiAuthNeedUSB.Error()})
				return
			}
			dev := g.Cfg.Device(udid)
			if dev == nil {
				g.Cfg.Devices = append(g.Cfg.Devices, Device{UDID: udid, Port: 8100})
				dev = g.Cfg.Device(udid)
			}
			// 手机端两开关均已开启：跳过写入，避免重复弹密码框。
			if conn, dbg, ok := wifiLockdownStatus(udid); alreadyWifiAuthorized(conn, dbg, ok) {
				dev.WifiDebug = true
				_ = g.Cfg.Save()
				slog.Info("wifi already authorized, skipped write", "udid", shortOf(udid))
				writeJSON(w, map[string]any{"udid": udid, "wifi_debug": true, "already_authorized": true, "need_wifi_auth": false})
				return
			}
			if err := enableWifiLockdown(udid); err != nil {
				body := map[string]any{"error": err.Error()}
				if isDevicePasscodeErr(err) {
					body["need_passcode"] = true
				}
				writeJSONStatus(w, http.StatusBadRequest, body)
				return
			}
			dev.WifiDebug = true
			_ = g.Cfg.Save()
			writeJSON(w, map[string]any{"udid": udid, "wifi_debug": true, "already_authorized": false, "need_wifi_auth": false})
		case "stop":
			dev := g.Cfg.Device(udid)
			if dev != nil {
				dev.AutoReactivate = false
				// 停止后立刻视为未激活，避免残留 last_health.ok 把删除继续拦住。
				applyHealth(dev, WDAHealth{OK: false, Error: "stopped"})
				_ = g.Cfg.Save()
			}
			stopped := g.WDA.Stop(udid)
			dropTunnel(udid)
			writeJSON(w, map[string]any{"udid": udid, "status": "stopped", "auto_reactivate": false, "stopped": stopped})
		case "delete":
			// 仅未激活设备可物理删除；已激活必须先停止。USB 仍连接时下一轮发现会重新出现。
			removed, stopped, err := g.purgeUnactivatedDevice(udid)
			if err != nil {
				writeJSONStatus(w, http.StatusConflict, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, map[string]any{
				"udid": udid, "status": "deleted", "removed": removed, "stopped": stopped,
			})
		case "health":
			dev := g.Cfg.Device(udid)
			if dev == nil || dev.IP == "" {
				writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "device has no ip configured"})
				return
			}
			writeJSON(w, g.checkWDA(dev))
		case "report":
			var body struct {
				SentOK   int     `json:"sent_ok"`
				SentFail int     `json:"sent_fail"`
				BatchID  string  `json:"batch_id"`
				Time     float64 `json:"time"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			// 记录到 executor 统计
			for i := 0; i < body.SentOK; i++ {
				g.Exec.recordMetric(udid, body.BatchID, "sent", false)
			}
			for i := 0; i < body.SentFail; i++ {
				g.Exec.recordMetric(udid, body.BatchID, "failed", false)
			}
			writeJSON(w, g.Exec.Metrics(udid))
		case "metrics":
			writeJSON(w, g.Exec.Metrics(udid))
		case "summary":
			writeJSON(w, g.Exec.MetricsSummary())
		default:
			http.NotFound(w, r)
		}
	})

	// /api/login 与 /api/session 公开；/api/cloud/config 也公开（配置平台地址是登录的前置条件，
	// ws_url 填错时若要求登录会形成死锁）；写操作 CSRF 校验带同源/无会话放行（见 csrfOrSameOrigin）。
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicAPI := r.URL.Path == "/api/login" || r.URL.Path == "/api/session" || r.URL.Path == "/api/cloud/config"
		if !strings.HasPrefix(r.URL.Path, "/api/") || publicAPI {
			if publicAPI && r.Method != http.MethodGet && r.Method != http.MethodHead && !csrfOrSameOrigin(auth, r) {
				writeJSONStatus(w, http.StatusForbidden, map[string]string{"error": "invalid csrf token"})
				return
			}
			mux.ServeHTTP(w, r)
			return
		}
		if !auth.Authenticated(r) {
			writeJSONStatus(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !auth.CSRFValid(r) {
			writeJSONStatus(w, http.StatusForbidden, map[string]string{"error": "invalid csrf token"})
			return
		}
		mux.ServeHTTP(w, r)
	}), nil
}

// csrfOrSameOrigin 公开配置接口的写保护：正常会话走 CSRF；未登录（拿不到 CSRF token，
// 典型为首次配置 ws_url）时按 Origin 同源放行；无会话 cookie 且无 Origin 的非浏览器
// 调用（curl 等）也放行。跨站 Origin 一律拒绝。
func csrfOrSameOrigin(auth *WebAuth, r *http.Request) bool {
	if auth.CSRFValid(r) {
		return true
	}
	hasSessionCookie := false
	if c, err := r.Cookie("wda_gateway_session"); err == nil && c.Value != "" {
		hasSessionCookie = true
	}
	origin := r.Header.Get("Origin")
	if origin == "" && !hasSessionCookie {
		return true // 非浏览器调用（无 cookie 无 Origin）
	}
	for _, h := range []string{"Origin", "Referer"} {
		if v := r.Header.Get(h); v != "" {
			if u, err := url.Parse(v); err == nil && u.Host == r.Host {
				return true // 同源页面（未登录时无 CSRF token 可用）
			}
		}
	}
	return false
}

func (g *Gateway) waitWDAReady(udid string, port int, timeout time.Duration, via string) map[string]any {
	via = parseActivateVia(via)
	deadline := time.Now().Add(timeout)
	probeOK := func(dev *Device) (WDAHealth, bool) {
		if dev == nil {
			return WDAHealth{}, false
		}
		if via == activateViaUSB && tunnelAddrForVia(udid, activateViaUSB) == "" {
			return WDAHealth{}, false
		}
		if via == activateViaNetwork && tunnelAddrForVia(udid, activateViaNetwork) == "" && dev.IP == "" {
			return WDAHealth{}, false
		}
		h := g.checkWDA(dev)
		return h, h.OK
	}
	for time.Now().Before(deadline) {
		dev := g.Cfg.Device(udid)
		if h, ok := probeOK(dev); ok {
			applyHealth(dev, h)
			if h.IP != "" {
				_ = syncStoredWifiIP(dev, h.IP)
				_ = g.Cfg.Save()
			}
			return map[string]any{"udid": udid, "status": "activated", "ready": true, "auto_reactivate": true, "via": via, "activate_via": via}
		}
		if !g.WDA.Running(udid) {
			if h, ok := probeOK(dev); ok {
				applyHealth(dev, h)
				if h.IP != "" {
					_ = syncStoredWifiIP(dev, h.IP)
					_ = g.Cfg.Save()
				}
				return map[string]any{"udid": udid, "status": "activated", "ready": true, "auto_reactivate": true, "via": via, "activate_via": via}
			}
			msg := "WDA 未能保持运行（进程已退出且本通道 /status 不通）"
			if via == activateViaUSB {
				msg += "。USB 激活需要 USB 连接，不会改走 Network"
			} else {
				msg += "。Network 激活需要 usbmux Network 或已记录的 Wi-Fi IP，不会回退 USB"
			}
			return map[string]any{
				"udid": udid, "status": "failed", "ready": false, "auto_reactivate": true,
				"message": msg,
			}
		}
		time.Sleep(400 * time.Millisecond)
	}
	if dev := g.Cfg.Device(udid); dev != nil {
		if h, ok := probeOK(dev); ok {
			applyHealth(dev, h)
			if h.IP != "" {
				_ = syncStoredWifiIP(dev, h.IP)
				_ = g.Cfg.Save()
			}
			return map[string]any{"udid": udid, "status": "activated", "ready": true, "auto_reactivate": true, "via": via, "activate_via": via}
		}
	}
	msg := "WDA 仍在启动，请稍候刷新"
	if via == activateViaUSB {
		msg = "USB 激活仍在启动；不会改走 Network"
	} else {
		msg = "Network 激活仍在启动（可能在等 usbmux Network 或手机锁屏密码）；不会回退 USB"
	}
	return map[string]any{
		"udid": udid, "status": "starting", "ready": false, "auto_reactivate": true,
		"message": msg, "via": via, "activate_via": via,
	}
}

// ensureUSBTunnelsForList builds iproxy for configured + currently attached USB/Network devices.

// healthCheckStale reports whether last_health is missing or older than maxAgeSec (failed checks are retried).
func healthCheckStale(h map[string]any, maxAgeSec float64) bool {
	if h == nil {
		return true
	}
	at, _ := h["checked_at"].(float64)
	if at == 0 {
		return true
	}
	return float64(time.Now().Unix())-at >= maxAgeSec
}

func (g *Gateway) ensureUSBTunnelsForList() {
	ports := map[string]int{}
	for _, d := range g.Cfg.Devices {
		if d.UDID == "" {
			continue
		}
		p := d.Port
		if p == 0 {
			p = 8100
		}
		ports[d.UDID] = p
	}
	addUsbmuxTunnelPorts(ports)
	EnsureUSBTunnels(ports, tunnelVias(g.Cfg.Devices))
}

// recognizeLiveWDA probes /status; when OK, marks device activated and stores Wi-Fi IP from ios.ip.
func (g *Gateway) recognizeLiveWDA(dev *Device) WDAHealth {
	if dev == nil {
		return WDAHealth{OK: false, Error: "nil device"}
	}
	h := g.checkWDA(dev)
	applyHealth(dev, h)
	if h.OK && h.IP != "" {
		if syncStoredWifiIP(dev, h.IP) {
			_ = g.Cfg.Save()
		}
	}
	return h
}

func (g *Gateway) deviceList() []map[string]any {
	// USB 直连与 usbmux Network / Wi-Fi（WDA 健康）实时同步；掉线设备不出现在列表。
	found := Discover()
	presentSet := map[string]bool{}
	usbInfo := map[string]DiscoveredDevice{}
	for _, d := range found {
		presentSet[udidKey(d.UDID)] = true
		usbInfo[udidKey(d.UDID)] = d
	}
	// Windows netmuxd 通过 mDNS 发现的 Network 设备：USB 不在也保持列表可见，
	// 用户可点 Network 激活（netmuxd 负责无线转发与心跳保活）。
	netSet := usbmuxNetworkUDIDs()
	for u := range netSet {
		presentSet[u] = true
	}
	usbSet := map[string]bool{}
	for _, u := range USBUDIDs() {
		usbSet[udidKey(u)] = true
	}
	// Windows 无 usbmux Network 条目：已配置设备（记录过 VendorUUID）拔线后，
	// 只要手机 Wi-Fi 上 WDA 在跑，就靠局域网扫描把它带回列表并恢复 IP。
	var wifiByUDID map[string]FoundWDA
	for i := range g.Cfg.Devices {
		if g.Cfg.Devices[i].VendorUUID != "" {
			wifiByUDID = wifiMatchByVendorUUID(g.Cfg.Devices, wifiScanned())
			break
		}
	}
	g.ensureUSBTunnelsForList()
	tunnelSet := liveGoIOSTunnelSet()
	out := []map[string]any{}
	emitted := map[string]bool{}
	for i := range g.Cfg.Devices {
		d := &g.Cfg.Devices[i]
		if d.UDID == "" {
			continue
		}
		name, model := d.Name, d.Model
		if info, ok := usbInfo[udidKey(d.UDID)]; ok {
			if name == "" {
				name = info.Name
			}
			if model == "" {
				model = info.Model
			}
		}
		present, attached, conn := muxPresence(udidKey(d.UDID), usbSet, presentSet, usbTunnelAlive(d.UDID), TunnelAddr(d.UDID) != "")
		if !present {
			if f, ok := wifiByUDID[udidKey(d.UDID)]; ok {
				present = true
				conn = "wifi"
				if ip := preferredWifiIP(f.IP, f.IOSIP); ip != "" && ip != d.IP {
					d.IP = ip
					d.Port = 8100
					_ = g.Cfg.Save()
					slog.Info("wifi device seen via LAN scan", "udid", shortOf(d.UDID), "ip", ip)
				}
			}
		}
		// USB/tunnel/IP available but not yet healthy: probe /status (throttled) and adopt as activated when ready.
		// 用户点停止后不再用残留 /status 把设备救活。
		if !userStopped(d, g.WDA.Running(d.UDID)) && (present || d.IP != "") && !healthOK(d.LastHealth) && !g.Exec.IsBusy(d.UDID) && healthCheckStale(d.LastHealth, 8) {
			g.recognizeLiveWDA(d)
		}
		busy := g.Exec.IsBusy(d.UDID)
		running := g.WDA.Running(d.UDID)
		if deviceAbsent(present, healthOK(d.LastHealth), busy, running) {
			continue
		}
		iosVer := d.IOSVersion
		if iosVer == "" {
			iosVer = cachedIOSVersion(d.UDID)
		}
		needTun := needsRemoteXPCTunnel(iosVer)
		out = append(out, map[string]any{
			"udid": d.UDID, "serial": g.SerialOf(d.UDID), "name": name, "model": model,
			"ip": d.IP, "port": d.Port, "auto_reactivate": d.AutoReactivate,
			"activate_via": parseActivateVia(d.ActivateVia),
			"last_health":  d.LastHealth, "ios_version": iosVer,
			"configured": true, "usb": attached, "conn_type": conn,
			"wda_running": wdaAppearsRunning(running, d.LastHealth),
			"metrics":     g.Exec.Metrics(d.UDID), "busy": busy,
			"deletable":      deviceDeletable(busy, healthOK(d.LastHealth), running),
			"needs_tunnel":   needTun,
			"tunnel_ready":   needTun && tunnelSet[strings.ToUpper(d.UDID)],
			"unplug_safe":    unplugSafeFor(d.UDID, iosVer, tunnelSet, netSet),
			"wifi_debug":     d.WifiDebug,
			"need_wifi_auth": needWifiAuth(attached, wifiAuthorized(d.UDID, d.WifiDebug)),
		})
		emitted[udidKey(d.UDID)] = true
	}
	for _, d := range found {
		if emitted[udidKey(d.UDID)] {
			continue
		}
		usb := d.Conn == "usb" || usbSet[udidKey(d.UDID)]
		conn := "wifi"
		if usb {
			conn = "usb"
		}
		// Unconfigured device: if phone WDA /status is already up, adopt it (IP + activated) instead of showing "activate".
		via := activateViaUSB
		if !usb {
			via = activateViaNetwork
		}
		dev := &Device{UDID: d.UDID, Port: 8100, Name: d.Name, Model: d.Model, AutoReactivate: true, ActivateVia: via}
		h := g.recognizeLiveWDA(dev)
		if h.OK {
			if existing := g.Cfg.Device(d.UDID); existing == nil {
				g.Cfg.Devices = append(g.Cfg.Devices, *dev)
			} else {
				applyHealth(existing, h)
				if h.IP != "" {
					_ = syncStoredWifiIP(existing, h.IP)
				}
				if existing.Name == "" && d.Name != "" {
					existing.Name = d.Name
				}
				if existing.Model == "" && d.Model != "" {
					existing.Model = d.Model
				}
				dev = existing
			}
			_ = g.Cfg.Save()
			busy := g.Exec.IsBusy(d.UDID)
			iosVer := dev.IOSVersion
			if iosVer == "" {
				iosVer = cachedIOSVersion(d.UDID)
			}
			needTun := needsRemoteXPCTunnel(iosVer)
			out = append(out, map[string]any{
				"udid": d.UDID, "serial": g.SerialOf(d.UDID), "name": dev.Name, "model": dev.Model,
				"ip": dev.IP, "port": dev.Port, "auto_reactivate": true, "configured": true,
				"activate_via": parseActivateVia(dev.ActivateVia),
				"last_health":  dev.LastHealth, "ios_version": iosVer,
				"usb": usb, "conn_type": conn,
				"wda_running": true,
				"metrics":     g.Exec.Metrics(d.UDID), "busy": busy,
				"deletable":      deviceDeletable(busy, true, true),
				"needs_tunnel":   needTun,
				"tunnel_ready":   needTun && tunnelSet[strings.ToUpper(d.UDID)],
				"unplug_safe":    unplugSafeFor(d.UDID, iosVer, tunnelSet, netSet),
				"wifi_debug":     dev.WifiDebug,
				"need_wifi_auth": needWifiAuth(usb, wifiAuthorized(d.UDID, dev.WifiDebug)),
			})
			continue
		}
		busy := g.Exec.IsBusy(d.UDID)
		running := g.WDA.Running(d.UDID)
		iosVer := cachedIOSVersion(d.UDID)
		needTun := needsRemoteXPCTunnel(iosVer)
		out = append(out, map[string]any{
			"udid": d.UDID, "serial": g.SerialOf(d.UDID), "name": d.Name, "model": d.Model,
			"ip": "", "port": 8100, "auto_reactivate": false, "configured": false,
			"activate_via": parseActivateVia(dev.ActivateVia),
			"usb":          usb, "conn_type": conn, "wda_running": running,
			"last_health": dev.LastHealth,
			"ios_version": iosVer,
			"metrics":     g.Exec.Metrics(d.UDID), "busy": busy,
			"deletable":      deviceDeletable(busy, healthOK(dev.LastHealth), running),
			"needs_tunnel":   needTun,
			"tunnel_ready":   needTun && tunnelSet[strings.ToUpper(d.UDID)],
			"unplug_safe":    unplugSafeFor(d.UDID, iosVer, tunnelSet, netSet),
			"wifi_debug":     false,
			"need_wifi_auth": needWifiAuth(usb, wifiAuthorized(d.UDID, false)),
		})
	}
	return out
}

// loopbackRequest 判断请求是否来自本机回环地址（RemoteAddr 形如 127.0.0.1:xxx）。
func loopbackRequest(r *http.Request) bool {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host == "127.0.0.1" || host == "[::1]" || host == "::1" || host == "localhost"
}

func writeJSON(w http.ResponseWriter, v any) {
	writeJSONStatus(w, http.StatusOK, v)
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteStatic 把静态文件目录写入当前目录（供 cmd 生成默认 index.html）。
func WriteStatic(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	index := filepath.Join(dir, "index.html")
	if _, err := os.Stat(index); err == nil {
		return nil
	}
	return os.WriteFile(index, []byte(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>WDA Farm Gateway</title></head><body><h3>WDA Farm Gateway</h3><p>设备列表/云通道状态见 <a href="/api/devices">/api/devices</a> 与 <a href="/api/cloud">/api/cloud</a>。</p></body></html>`), 0o644)
}
