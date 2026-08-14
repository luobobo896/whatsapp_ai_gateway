package gateway

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Handler 返回网关 HTTP 路由（REST + 静态页）。
func (g *Gateway) Handler(staticDir string) http.Handler {
	mux := http.NewServeMux()
	auth := NewWebAuth(g.Cfg)

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
			} else {
				dev.AutoReactivate = true
				_ = g.Cfg.Save()
			}
			// WDA 已健康（外部工具/手工启动的场景）：直接按已激活返回，
			// 不再重复拉起 xcodebuild 与现有 WDA 抢 8100 端口。
			if dev.IP != "" {
				if h := CheckWDA(dev.IP, port, 3*time.Second); h.OK {
					applyHealth(dev, h)
					writeJSON(w, map[string]any{"udid": udid, "status": "activated", "ready": true, "auto_reactivate": true, "via": "already-running"})
					return
				}
			}
			if err := g.WDA.Activate(udid, port, udid); err != nil {
				writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "激活失败：" + err.Error()})
				return
			}
			// 等待就绪（最多 60s；未就绪返回 starting，由前端轮询/看护循环继续跟进）
			res := g.waitWDAReady(udid, port, 60*time.Second)
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
			// 删除 = 移除配置（IP/身份/自动拉起）并停掉网关托管的 WDA 进程。
			// USB 仍连接的设备会以「未配置」身份重新出现在列表（发现层自动恢复，防误删）；
			// 未插 USB 的设备删除后即从列表消失。
			if g.Exec.IsBusy(udid) {
				writeJSONStatus(w, http.StatusConflict, map[string]string{"error": "设备正在执行任务，不能删除"})
				return
			}
			stopped := g.WDA.Stop(udid)
			removed := g.Cfg.RemoveDevice(udid)
			if !removed {
				writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "设备不在配置中（USB 设备无需删除，拔线即消失）"})
				return
			}
			writeJSON(w, map[string]any{
				"udid": udid, "status": "deleted", "removed": true, "stopped": stopped,
				"usb_reappears": usbConnected(udid),
			})
		case "health":
			dev := g.Cfg.Device(udid)
			if dev == nil || dev.IP == "" {
				writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "device has no ip configured"})
				return
			}
			writeJSON(w, CheckWDA(dev.IP, dev.Port, 3*time.Second))
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

	// /api/login 与 /api/session 公开；其余 /api/* 需会话；state-changing 方法另需 CSRF。
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api/login" || r.URL.Path == "/api/session" {
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
	})
}

func (g *Gateway) waitWDAReady(udid string, port int, timeout time.Duration) map[string]any {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !g.WDA.Running(udid) {
			return map[string]any{"udid": udid, "status": "failed", "ready": false, "auto_reactivate": true}
		}
		dev := g.Cfg.Device(udid)
		if dev != nil && dev.IP != "" {
			h := CheckWDA(dev.IP, port, 3*time.Second)
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
	// 设备列表实时获取：USB 直连（ioreg/devicectl）与 Wi-Fi 在线（WDA 健康）才算在线；
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
		if d.UDID == "" {
			continue
		}
		if !usbSet[d.UDID] && !healthOK(d.LastHealth) {
			continue // 离线：无 USB 且 Wi-Fi 不可达
		}
		name, model := d.Name, d.Model
		if info, ok := usbInfo[d.UDID]; ok {
			if name == "" {
				name = info.Name
			}
			if model == "" {
				model = info.Model
			}
		}
		out = append(out, map[string]any{
			"udid": d.UDID, "serial": g.SerialOf(d.UDID), "name": name, "model": model,
			"ip": d.IP, "port": d.Port, "auto_reactivate": d.AutoReactivate,
			"last_health": d.LastHealth, "ios_version": d.IOSVersion,
			"configured": true, "usb": usbSet[d.UDID],
			"wda_running": g.WDA.Running(d.UDID),
			"metrics":     g.Exec.Metrics(d.UDID), "busy": g.Exec.IsBusy(d.UDID),
		})
		emitted[d.UDID] = true
	}
	for _, d := range usb {
		if emitted[d.UDID] {
			continue
		}
		out = append(out, map[string]any{
			"udid": d.UDID, "serial": g.SerialOf(d.UDID), "name": d.Name, "model": d.Model,
			"ip": "", "port": 8100, "auto_reactivate": false, "configured": false,
			"usb": true, "wda_running": g.WDA.Running(d.UDID),
			"metrics": g.Exec.Metrics(d.UDID), "busy": g.Exec.IsBusy(d.UDID),
		})
	}
	return out
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
