package gateway

import (
	"context"
	"log/slog"
	"time"

	"wda-farm-gateway/internal/wda"
)

// tapAgentPermissions 激活成功后点 Device Agent 的网络等权限按钮。
// 当前 Agent 已去掉扫码注册，这里不点注册/扫码。
func (g *Gateway) tapAgentPermissions(udid string, port int) {
	dev := g.Cfg.Device(udid)
	ip := ""
	if dev != nil {
		ip = dev.IP
		if port == 0 {
			port = dev.Port
		}
	}
	if port == 0 {
		port = 8100
	}
	base := resolveWDABaseURL(udid, ip, port)
	if base == "" || base == "http://:8100" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := wda.NewClient(base, 4*time.Second)
	wda.TapPermissionAllows(ctx, client)
	team := ""
	if g.WDA != nil {
		team = teamNameFromIPA(g.WDA.resolvedIPA())
	}
	if err := wda.TrustDeveloper(ctx, client, team); err != nil {
		slog.Warn("auto trust developer skipped", "udid", shortOf(udid), "error", err)
	}
	slog.Info("tapped agent permission and developer trust", "udid", shortOf(udid))
}
