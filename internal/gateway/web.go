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
			"tenant_id": tid, "tenant_name": tname, "user_email": uemail, "user_name": uname,
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
			if err := g.WDA.Activate(udid, port, udid); err != nil {
				writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "激活失败：" + err.Error()})
				return
			}
			// 等待就绪（最多 180s）
			res := g.waitWDAReady(udid, port, 180*time.Second)
			writeJSON(w, res)
		case "stop":
			dev := g.Cfg.Device(udid)
			if dev != nil {
				dev.AutoReactivate = false
				_ = g.Cfg.Save()
			}
			stopped := g.WDA.Stop(udid)
			writeJSON(w, map[string]any{"udid": udid, "status": "stopped", "auto_reactivate": false, "stopped": stopped})
		case "health":
			dev := g.Cfg.Device(udid)
			if dev == nil || dev.IP == "" {
				writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "device has no ip configured"})
				return
			}
			writeJSON(w, CheckWDA(dev.IP, dev.Port, 3*time.Second))
		case "set-ip":
			ip := r.URL.Query().Get("ip")
			port, _ := strconv.Atoi(r.URL.Query().Get("port"))
			if port == 0 {
				port = 8100
			}
			dev := g.Cfg.Device(udid)
			if dev == nil {
				dev = &Device{UDID: udid, AutoReactivate: true}
				g.Cfg.Devices = append(g.Cfg.Devices, *dev)
			}
			dev.IP, dev.Port = ip, port
			_ = g.Cfg.Save()
			writeJSON(w, map[string]any{"udid": udid, "ip": ip, "port": port})
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
				g.Exec.recordMetric(udid, body.BatchID, "sent")
			}
			for i := 0; i < body.SentFail; i++ {
				g.Exec.recordMetric(udid, body.BatchID, "failed")
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

	return mux
}

func (g *Gateway) waitWDAReady(udid string, port int, timeout time.Duration) map[string]any {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !g.WDA.Running(udid) {
			return map[string]any{"udid": udid, "status": "failed", "ready": false, "auto_reactivate": true}
		}
		if dev := g.Cfg.Device(udid); dev != nil && dev.IP != "" {
			h := CheckWDA(dev.IP, port, 3*time.Second)
			if h.OK {
				if dev != nil {
					dev.LastHealth = map[string]any{"ok": true, "ready": true, "ip": h.IP, "ios_version": h.Version, "checked_at": float64(time.Now().Unix()), "starting": false}
				}
				return map[string]any{"udid": udid, "status": "activated", "ready": true, "auto_reactivate": true}
			}
		}
		time.Sleep(3 * time.Second)
	}
	return map[string]any{"udid": udid, "status": "starting", "ready": false, "auto_reactivate": true}
}

func (g *Gateway) deviceList() []map[string]any {
	out := []map[string]any{}
	configured := map[string]bool{}
	for _, d := range g.Cfg.Devices {
		configured[d.UDID] = true
		entry := map[string]any{
			"udid": d.UDID, "name": d.Name, "model": d.Model,
			"ip": d.IP, "port": d.Port, "auto_reactivate": d.AutoReactivate,
			"last_health": d.LastHealth, "ios_version": d.IOSVersion,
			"configured": true, "wda_running": g.WDA.Running(d.UDID),
			"metrics": g.Exec.Metrics(d.UDID), "busy": g.Exec.IsBusy(d.UDID),
		}
		out = append(out, entry)
	}
	for _, d := range Discover() {
		if configured[d.UDID] {
			continue
		}
		out = append(out, map[string]any{
			"udid": d.UDID, "name": d.Name, "model": d.Model,
			"ip": "", "port": 8100, "auto_reactivate": false, "configured": false,
			"wda_running": g.WDA.Running(d.UDID), "metrics": g.Exec.Metrics(d.UDID),
			"busy": g.Exec.IsBusy(d.UDID),
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
