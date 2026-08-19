package gateway

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"net/http/pprof"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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
			http.ServeFile(w, r, filepath.Join(staticDir, "index.html"))
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

	mux.HandleFunc("/api/devices/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/devices/")
		// 隐藏（已删除）设备列表：供前端渲染"已隐藏设备"恢复入口。
		if rest == "ignored" {
			if r.Method != http.MethodGet {
				writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
				return
			}
			writeJSON(w, g.Cfg.Ignored)
			return
		}
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
			dev := g.Cfg.Device(udid)
			if dev == nil {
				dev = &Device{UDID: udid, Port: port, AutoReactivate: true}
				g.Cfg.Devices = append(g.Cfg.Devices, *dev)
				// 新设备立即持久化（此前依赖 UnignoreDevice 的隐式 saveLocked，
				// 若隐藏列表已无该设备导致逻辑分支异常，auto_reactivate 可能丢失，
				// 重启后 watchdog 不再自动拉起）。
				_ = g.Cfg.Save()
			} else {
				dev.AutoReactivate = true
				_ = g.Cfg.Save()
			}
			// 激活即恢复显示：从隐藏列表移除，USB 在线设备重新出现在列表。
			_ = g.Cfg.UnignoreDevice(udid)
			// WDA 已健康（外部工具/手工启动的场景）：直接按已激活返回，
			// 不再重复拉起 xcodebuild 与现有 WDA 抢 8100 端口。
			if dev.IP != "" {
				if h := g.checkWDA(dev); h.OK {
					applyHealth(dev, h)
					writeJSON(w, map[string]any{"udid": udid, "status": "activated", "ready": true, "auto_reactivate": true, "via": "already-running"})
					return
				}
			}
			// 手动激活允许清除崩溃冷却（人工处理签名/信任问题后立即重试）。
			g.WDA.ResetCrashCooldown(udid)
			if err := g.WDA.Activate(udid, port, udid); err != nil {
				writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "激活失败：" + err.Error()})
				return
			}
			// 等待就绪（最多 60s；未就绪返回 starting，由前端轮询/看护循环继续跟进）
			res := g.waitWDAReady(udid, port, 60*time.Second)
			g.KickWatchdog() // 立即跑一轮看护：USB 隧道问 WDA 自报 IP，秒级完成自动分配
			writeJSON(w, res)
		case "stop":
			dev := g.Cfg.Device(udid)
			if dev != nil {
				dev.AutoReactivate = false
				_ = g.Cfg.Save()
			}
			stopped := g.WDA.Stop(udid)
			writeJSON(w, map[string]any{"udid": udid, "status": "stopped", "auto_reactivate": false, "stopped": stopped})
		case "delete":
			// 删除 = 移除配置（IP/身份/自动拉起）+ 停掉网关托管的 WDA 进程 + 加入隐藏列表。
			// 隐藏后即使 USB 仍连接也不在设备列表出现（此前 USB 在线设备会以「未配置」
			// 身份立刻重新出现，用户反馈"删除没用"）；重新激活/恢复后重新显示。
			if g.Exec.IsBusy(udid) {
				writeJSONStatus(w, http.StatusConflict, map[string]string{"error": "设备正在执行任务，不能删除"})
				return
			}
			stopped := g.WDA.Stop(udid)
			removed := g.Cfg.RemoveDevice(udid)
			_ = g.Cfg.IgnoreDevice(udid)
			writeJSON(w, map[string]any{
				"udid": udid, "status": "deleted", "removed": removed, "stopped": stopped,
				"hidden": true,
			})
		case "unignore":
			// 恢复显示被手动删除（隐藏）的设备：仅取消隐藏，不自动激活。
			// USB 在线设备随即以「未配置」状态重新出现在列表，可再点激活。
			if r.Method != http.MethodPost {
				writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
				return
			}
			_ = g.Cfg.UnignoreDevice(udid)
			writeJSON(w, map[string]any{"udid": udid, "status": "unignored", "visible": true})
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

func (g *Gateway) waitWDAReady(udid string, port int, timeout time.Duration) map[string]any {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !g.WDA.Running(udid) {
			return map[string]any{"udid": udid, "status": "failed", "ready": false, "auto_reactivate": true}
		}
		dev := g.Cfg.Device(udid)
		if dev != nil && dev.IP != "" {
			h := g.checkWDA(dev)
			if h.OK {
				dev.LastHealth = map[string]any{"ok": true, "ready": true, "ip": h.IP, "ios_version": h.Version, "checked_at": float64(time.Now().Unix()), "starting": false}
				return map[string]any{"udid": udid, "status": "activated", "ready": true, "auto_reactivate": true}
			}
		} else {
			// USB 直连、尚未配 IP：WDA 进程已运行即视为激活成功；
			// 手机 Wi-Fi IP 由 watchdog 的 LAN 扫描自动发现，无需手动设置。
			return map[string]any{"udid": udid, "status": "activated", "ready": true, "auto_reactivate": true, "via": "usb"}
		}
		time.Sleep(3 * time.Second)
	}
	return map[string]any{"udid": udid, "status": "starting", "ready": false, "auto_reactivate": true}
}

func (g *Gateway) deviceList() []map[string]any {
	// 设备列表实时获取：USB 直连（idevice_id/ioreg/devicectl）与 Wi-Fi 在线（WDA 健康）才算在线；
	// 离线设备不再出现（不在线就没有数据）。
	usb := Discover()
	usbSet := map[string]bool{}
	usbInfo := map[string]DiscoveredDevice{}
	for _, d := range usb {
		usbSet[d.UDID] = true
		usbInfo[d.UDID] = d
	}

	out := []map[string]any{}
	emitted := map[string]bool{}
	for i := range g.Cfg.Devices {
		d := &g.Cfg.Devices[i]
		if d.UDID == "" || g.Cfg.IsIgnored(d.UDID) {
			continue
		}
		// 已配置设备始终显示：拔掉 USB 后设备转 Wi-Fi（或离线）不消失，
		// 在线状态由 last_health 表达；彻底离线的设备仍保留，可手动删除。
		// （此前 `!usbSet && !healthOK` 直接把离线设备从列表/云上报过滤，
		//   导致拔 USB 且 Wi-Fi 探活失败时设备“被自动删除”。）
		name, model := d.Name, d.Model
		if info, ok := usbInfo[d.UDID]; ok {
			if name == "" {
				name = info.Name
			}
			if model == "" {
				model = info.Model
			}
		}
		conn := "wifi"
		if usbSet[d.UDID] {
			conn = "usb"
		}
		out = append(out, map[string]any{
			"udid": d.UDID, "serial": g.SerialOf(d.UDID), "name": name, "model": model,
			"ip": d.IP, "port": d.Port, "auto_reactivate": d.AutoReactivate,
			"last_health": d.LastHealth, "ios_version": d.IOSVersion,
			"configured": true, "usb": usbSet[d.UDID], "conn_type": conn,
			"wda_running": g.WDA.Running(d.UDID),
			"metrics":     g.Exec.Metrics(d.UDID), "busy": g.Exec.IsBusy(d.UDID),
		})
		emitted[d.UDID] = true
	}
	for _, d := range usb {
		if emitted[d.UDID] || g.Cfg.IsIgnored(d.UDID) {
			continue
		}
		out = append(out, map[string]any{
			"udid": d.UDID, "serial": g.SerialOf(d.UDID), "name": d.Name, "model": d.Model,
			"ip": "", "port": 8100, "auto_reactivate": false, "configured": false,
			"usb": true, "conn_type": "usb", "wda_running": g.WDA.Running(d.UDID),
			"metrics": g.Exec.Metrics(d.UDID), "busy": g.Exec.IsBusy(d.UDID),
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
