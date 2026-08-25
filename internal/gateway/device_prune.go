package gateway

import (
	"errors"
	"log/slog"
)

var (
	errDeviceBusy      = errors.New("设备正在执行任务，不能删除")
	errDeviceActivated = errors.New("已激活的设备不能删除，请先停止")
)

// deviceActivated 与页面「WDA 未激活」相对：主机进程在跑或 /status 已通，即视为已激活。
func deviceActivated(healthy, running bool) bool {
	return healthy || running
}

// deviceDeletable 仅未激活且未忙碌的设备可删。
func deviceDeletable(busy, healthy, running bool) bool {
	return !busy && !deviceActivated(healthy, running)
}

// deviceAbsent 判定设备已掉线：无 USB/Network/隧道、WDA 不健康、未在执行任务、主机也未托管激活进程。
// USB 或 usbmux Network 仍在但未激活不算掉线；忙碌或正在拉起时也不删，避免误伤。
func deviceAbsent(attached, healthy, busy, running bool) bool {
	return !attached && !healthy && !busy && !running
}

// pruneOfflineDevices 把已确认掉线的配置设备物理删除（无隐藏/恢复）。
// 看护循环在本轮探活与重激活尝试之后调用，给 USB→Wi-Fi 交接留出同一轮探测机会。
func (g *Gateway) pruneOfflineDevices(usbSet map[string]bool) []string {
	if g == nil || g.Cfg == nil {
		return nil
	}
	var gone []string
	for i := range g.Cfg.Devices {
		d := &g.Cfg.Devices[i]
		if d.UDID == "" {
			continue
		}
		// 已授权无线调试的设备随时可能从 Wi-Fi 回来：掉线只隐藏不物理删除，
		// 否则 iOS 空闲关闭无线会话后配置（activate_via/wifi_debug/ip）丢失，
		// 手机重新广播时还得插 USB 重新授权才能自愈。
		if d.WifiDebug {
			continue
		}
		attached := attachedUSB(d.UDID, usbSet, TunnelAddr(d.UDID) != "")
		busy := g.Exec != nil && g.Exec.IsBusy(d.UDID)
		running := g.WDA != nil && g.WDA.Running(d.UDID)
		if deviceAbsent(attached, healthOK(d.LastHealth), busy, running) {
			gone = append(gone, d.UDID)
		}
	}
	for _, u := range gone {
		if g.Cfg.RemoveDevice(u) {
			slog.Info("removed offline device", "udid", shortOf(u))
		}
	}
	return gone
}

func (g *Gateway) deviceLiveState(udid string) (busy, healthy, running bool) {
	if g.Exec != nil {
		busy = g.Exec.IsBusy(udid)
	}
	if g.WDA != nil {
		running = g.WDA.Running(udid)
	}
	if g.Cfg != nil {
		if d := g.Cfg.Device(udid); d != nil {
			healthy = healthOK(d.LastHealth)
		}
	}
	return busy, healthy, running
}

// purgeUnactivatedDevice 物理删除未激活设备：配置、运行时缓存、当日分设备统计一并清掉。
func (g *Gateway) purgeUnactivatedDevice(udid string) (removed, stopped bool, err error) {
	if udid == "" {
		return false, false, nil
	}
	busy, healthy, running := g.deviceLiveState(udid)
	if !deviceDeletable(busy, healthy, running) {
		if busy {
			return false, false, errDeviceBusy
		}
		return false, false, errDeviceActivated
	}
	if g.WDA != nil {
		stopped = g.WDA.Stop(udid)
	}
	if g.Cfg != nil {
		removed = g.Cfg.RemoveDevice(udid)
	}
	if g.Exec != nil {
		g.Exec.forgetMetrics(udid)
	}
	g.forgetRuntime(udid)
	slog.Info("purged unactivated device", "udid", shortOf(udid), "removed", removed, "stopped", stopped)
	return removed, stopped, nil
}

func (g *Gateway) forgetRuntime(udid string) {
	g.serialMu.Lock()
	delete(g.serials, udid)
	delete(g.serialTried, udid)
	g.serialMu.Unlock()

	g.statusMu.Lock()
	delete(g.lastStatus, udid)
	g.statusMu.Unlock()
}
