package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"wda-farm-gateway/internal/wda"
)

const wdaStartGrace = 90 * time.Second

// WatchdogLoop 逐台健康检查、自动重激活、网络跟随、自动配 IP。
func (g *Gateway) WatchdogLoop(ctx context.Context) {
	cfg := g.Cfg
	interval := time.Duration(cfg.HealthInterval) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}
	for {
		g.watchOnce()
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func (g *Gateway) watchOnce() {
	cfg := g.Cfg
	for i := range cfg.Devices {
		dev := &cfg.Devices[i]
		if dev.UDID == "" || dev.IP == "" {
			continue
		}
		h := CheckWDA(dev.IP, dev.Port, 3*time.Second)
		prevOK := healthOK(dev.LastHealth)
		dev.LastHealth = map[string]any{
			"ok": h.OK, "ready": h.Ready, "ip": h.IP,
			"ios_version": h.Version, "checked_at": float64(time.Now().Unix()),
			"starting": false, "error": h.Error,
		}
		if h.Version != "" {
			dev.IOSVersion = h.Version
		}
		// 在线时记录 WDA identifierForVendor(uuid)，供网络变化后按 uuid 重新匹配。
		if h.OK && dev.VendorUUID == "" {
			if uuid := g.vendorUUID(dev.IP, dev.Port); uuid != "" {
				dev.VendorUUID = uuid
				_ = cfg.Save()
				slog.Info("device recorded vendor_uuid", "udid", dev.UDID[:8], "uuid", uuid)
			}
		}
		if prevOK != h.OK && !g.Exec.IsBusy(dev.UDID) {
			st := "online"
			if !h.OK {
				st = "offline"
			}
			g.Exec.StatusQ <- DeviceStatus{UDID: dev.UDID, WDAStatus: st, Error: errText(h.Error)}
		}
		if !h.OK && dev.AutoReactivate && !g.WDA.Running(dev.UDID) {
			slog.Info("WDA down, reactivating", "udid", dev.UDID[:8], "ip", dev.IP)
			if err := g.WDA.Activate(dev.UDID, dev.Port, dev.UDID); err != nil {
				slog.Error("reactivate failed", "udid", dev.UDID[:8], "error", err)
			}
		}
	}
	_ = g.autoAssignIP()
	_ = g.followNetworkChange()
}

func (g *Gateway) vendorUUID(ip string, port int) string {
	if port == 0 {
		port = 8100
	}
	client := wda.NewClient(fmt.Sprintf("http://%s:%d", ip, port), 3*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	uuid, _, _, err := client.DeviceInfo(ctx)
	if err != nil {
		return ""
	}
	return uuid
}

// autoAssignIP 对「已激活但未配置 IP」的设备自动探测局域网 WDA。
func (g *Gateway) autoAssignIP() error {
	cfg := g.Cfg
	known := map[string]bool{}
	for _, d := range cfg.Devices {
		if d.IP != "" {
			known[d.IP] = true
		}
	}
	// 未配置 IP 且 WDA 已运行的设备
	var pending []string
	for _, d := range cfg.Devices {
		if d.IP == "" && g.WDA.Running(d.UDID) {
			pending = append(pending, d.UDID)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	found := ScanLANWDA(500 * time.Millisecond)
	var cands []FoundWDA
	for _, f := range found {
		if !known[f.IP] && f.IP != "127.0.0.1" {
			cands = append(cands, f)
		}
	}
	if len(pending) == 1 && len(cands) == 1 {
		if d := cfg.Device(pending[0]); d != nil {
			d.IP = cands[0].IP
			d.Port = 8100
			d.AutoReactivate = true
			_ = cfg.Save()
			slog.Info("auto-assigned WDA IP", "udid", pending[0][:8], "ip", cands[0].IP)
		}
	}
	return nil
}

// followNetworkChange Mac 换网后：IP 不可达的已配置设备按 uuid（或 ios版本+model）重新匹配到新 IP。
func (g *Gateway) followNetworkChange() error {
	cfg := g.Cfg
	var stale []*Device
	for i := range cfg.Devices {
		d := &cfg.Devices[i]
		if d.UDID != "" && d.IP != "" && !healthOK(d.LastHealth) {
			stale = append(stale, d)
		}
	}
	if len(stale) == 0 {
		return nil
	}
	found := ScanLANWDA(500 * time.Millisecond)
	if len(found) == 0 {
		return nil
	}
	byUUID := map[string]FoundWDA{}
	for _, f := range found {
		if f.UUID != "" {
			byUUID[f.UUID] = f
		}
	}
	for _, dev := range stale {
		old := dev.IP
		hit, ok := byUUID[dev.VendorUUID]
		if !ok {
			var cands []FoundWDA
			for _, f := range found {
				if f.IOSVersion != dev.IOSVersion {
					continue
				}
				if dev.Model != "" && f.Model != "" && f.Model != dev.Model {
					continue
				}
				cands = append(cands, f)
			}
			if len(cands) == 1 {
				hit = cands[0]
			} else if len(cands) > 1 {
				slog.Warn("new IP ambiguous, skip auto-follow", "udid", dev.UDID[:8], "candidates", len(cands))
				continue
			}
		}
		if hit.IP == "" || hit.IP == old {
			continue
		}
		dev.IP = hit.IP
		if hit.UUID != "" {
			dev.VendorUUID = hit.UUID
		}
		_ = cfg.Save()
		slog.Info("device followed network change", "udid", dev.UDID[:8], "old", old, "new", hit.IP)
		g.Exec.StatusQ <- DeviceStatus{UDID: dev.UDID, WDAStatus: "online", Error: "ip updated " + old + " -> " + hit.IP}
	}
	return nil
}

func healthOK(h map[string]any) bool {
	v, _ := h["ok"].(bool)
	return v
}

func errText(err string) string {
	if err == "" {
		return ""
	}
	return err
}
