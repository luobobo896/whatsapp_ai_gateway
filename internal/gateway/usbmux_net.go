package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// 自动修复相关节奏常量。
const (
	usbmuxNetCheckInterval      = 30 * time.Second // 后台检测周期
	usbmuxNetAutoMinInterval    = 2 * time.Minute  // 两次自动修复最小间隔（防抖）
	usbmuxNetVerifyTimeout      = 20 * time.Second // 修复后等待 Network 条目出现的总时长
	usbmuxNetVerifyPollInterval = 2 * time.Second  // 修复后轮询间隔
)

// usbmuxConnSets 是 usbmux 当前连接类型的快照。
// 同一 UDID 可能同时有 USB 与 Network 两条（拔线前双通道），因此分别记录。
type usbmuxConnSets struct {
	netSet map[string]bool // UDID(大写) -> 存在 Network 条目
	usbSet map[string]bool // UDID(大写) -> 存在 USB 条目
}

// usbmuxConnSetsRead 执行 `ios list --details` 并解析出 USB / Network 两个集合。
// 解析失败或工具缺失时返回空集合（调用方按“无 Network、也无 USB”处理，不误触发修复）。
func usbmuxConnSetsRead() usbmuxConnSets {
	out := usbmuxConnSets{netSet: map[string]bool{}, usbSet: map[string]bool{}}
	bin := lookTool("ios", "ios.exe")
	if bin == "" {
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "list", "--details")
	cmd.Env = append(os.Environ(), bundleLibFallback()...)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return out
	}
	arr := extractJSONArray(string(raw))
	if arr == "" {
		return out
	}
	var list []struct {
		Udid           string `json:"Udid"`
		ConnectionType string `json:"ConnectionType"`
	}
	if json.Unmarshal([]byte(arr), &list) != nil {
		return out
	}
	for _, d := range list {
		if d.Udid == "" {
			continue
		}
		u := strings.ToUpper(strings.TrimSpace(d.Udid))
		switch d.ConnectionType {
		case "Network":
			out.netSet[u] = true
		case "USB":
			out.usbSet[u] = true
		}
	}
	return out
}

// usbmuxNetNeedsRepair 判断是否存在“USB 已接入但缺 Network 条目”的设备。
// 只要有一台，重启系统 usbmuxd 就能让它重新走网络发现。
func usbmuxNetNeedsRepair(conns usbmuxConnSets) bool {
	for u := range conns.usbSet {
		if !conns.netSet[u] {
			return true
		}
	}
	return false
}

// usbmuxNetDeviceStatus 单台设备在“无线保活”视角下的状态。
type usbmuxNetDeviceStatus struct {
	UDID       string `json:"udid"`
	Name       string `json:"name,omitempty"`
	IP         string `json:"ip,omitempty"`
	Connection string `json:"connection"` // Network | UsbOnly | Absent
	UnplugSafe bool   `json:"unplug_safe"`
}

// usbmuxNetStatus 是 /api/usbmux-net 返回的整体状态。
type usbmuxNetStatus struct {
	AutoRepair  bool                    `json:"auto_repair"`
	Total       int                     `json:"total"`
	Network     int                     `json:"network"`
	UsbOnly     int                     `json:"usb_only"`
	Absent      int                     `json:"absent"`
	UnplugSafe  int                     `json:"unplug_safe"`
	NeedsRepair bool                    `json:"needs_repair"`
	LastRestart string                  `json:"last_restart,omitempty"`
	LastResult  string                  `json:"last_result,omitempty"`
	Devices     []usbmuxNetDeviceStatus `json:"devices"`
}

var usbmuxNetConnOrder = map[string]int{"Network": 0, "UsbOnly": 1, "Absent": 2}

// usbmuxNetStatus 汇总：遍历网关管理的设备，标记每条连接类型与是否可拔线。
func (g *Gateway) usbmuxNetStatus() usbmuxNetStatus {
	conns := usbmuxConnSetsRead()
	st := buildUsbmuxNetStatus(g.Cfg.Devices, conns, g.Cfg.UsbmuxNet.AutoRepair)
	g.usbmuxMu.Lock()
	// 与设备列表一致：usbmux 上已无设备时清掉「仍有 N 台缺 Network」残留。
	if st.Total == 0 {
		g.lastUsbmuxResult = ""
	}
	if !g.lastUsbmuxRepair.IsZero() && st.Total > 0 {
		st.LastRestart = g.lastUsbmuxRepair.Format(time.RFC3339)
	}
	if st.Total > 0 {
		st.LastResult = g.lastUsbmuxResult
	}
	g.usbmuxMu.Unlock()
	return st
}

// buildUsbmuxNetStatus 纯函数：由设备列表 + usbmux 连接集计算状态（便于单测，不触网）。
func buildUsbmuxNetStatus(devices []Device, conns usbmuxConnSets, autoRepair bool) usbmuxNetStatus {
	st := usbmuxNetStatus{AutoRepair: autoRepair, Devices: []usbmuxNetDeviceStatus{}}
	for _, d := range devices {
		u := strings.ToUpper(strings.TrimSpace(d.UDID))
		if u == "" {
			continue
		}
		sd := usbmuxNetDeviceStatus{UDID: d.UDID, Name: d.Name, IP: d.IP}
		switch {
		case conns.netSet[u]:
			sd.Connection = "Network"
			sd.UnplugSafe = true
		case conns.usbSet[u]:
			sd.Connection = "UsbOnly"
		default:
			// 拔线后 usbmux 上看不到：与设备列表一样不计入，避免 0/1 残留。
			continue
		}
		switch sd.Connection {
		case "Network":
			st.Network++
			st.UnplugSafe++
		case "UsbOnly":
			st.UsbOnly++
		default:
			st.Absent++
		}
		st.Devices = append(st.Devices, sd)
	}
	st.Total = len(st.Devices)
	st.NeedsRepair = usbmuxNetNeedsRepair(conns)
	sort.SliceStable(st.Devices, func(i, j int) bool {
		oi, oj := usbmuxNetConnOrder[st.Devices[i].Connection], usbmuxNetConnOrder[st.Devices[j].Connection]
		if oi != oj {
			return oi < oj
		}
		if st.Devices[i].Name != st.Devices[j].Name {
			return st.Devices[i].Name < st.Devices[j].Name
		}
		return st.Devices[i].UDID < st.Devices[j].UDID
	})
	return st
}

// usbmuxNetWaitForRepair 重启后轮询，直到“缺少 Network”问题消失或超时。
func usbmuxNetWaitForRepair(ctx context.Context) usbmuxConnSets {
	deadline := time.Now().Add(usbmuxNetVerifyTimeout)
	for {
		conns := usbmuxConnSetsRead()
		if !usbmuxNetNeedsRepair(conns) || time.Now().After(deadline) {
			return conns
		}
		select {
		case <-ctx.Done():
			return conns
		case <-time.After(usbmuxNetVerifyPollInterval):
		}
	}
}

func summarizeUsbmuxNetChange(after usbmuxConnSets) string {
	missing := 0
	for u := range after.usbSet {
		if !after.netSet[u] {
			missing++
		}
	}
	if missing == 0 {
		return "usbmuxd 已重启，全部 USB 设备均已挂上 Network 条目"
	}
	return fmt.Sprintf("usbmuxd 已重启，仍有 %d 台设备缺 Network 条目（需检查其 Wi-Fi 可达性与 EnableWifiDebugging）", missing)
}

func (g *Gateway) recordUsbmuxResult(status, msg string) {
	g.usbmuxMu.Lock()
	g.lastUsbmuxRepair = time.Now()
	g.lastUsbmuxResult = status + " · " + msg
	g.usbmuxMu.Unlock()
	if status == "error" {
		slog.Error("usbmux auto-repair", "status", status, "msg", msg)
	} else {
		slog.Info("usbmux auto-repair", "status", status, "msg", msg)
	}
}

// usbmuxNetRepair 执行一次修复（手动 / 自动共用）。返回 error 仅在“重启 usbmuxd 失败”。
func (g *Gateway) usbmuxNetRepair(ctx context.Context) error {
	g.usbmuxMu.Lock()
	if g.usbmuxRepairRun {
		g.usbmuxMu.Unlock()
		return errors.New("上一次修复仍在进行，请稍后再试")
	}
	g.usbmuxRepairRun = true
	g.usbmuxMu.Unlock()
	defer func() {
		g.usbmuxMu.Lock()
		g.usbmuxRepairRun = false
		g.usbmuxMu.Unlock()
	}()

	before := usbmuxConnSetsRead()
	if !usbmuxNetNeedsRepair(before) {
		g.recordUsbmuxResult("ok", "当前无缺 Network 条目的设备，无需修复")
		return nil
	}
	if err := restartUsbmuxd(); err != nil {
		g.recordUsbmuxResult("error", err.Error())
		return err
	}
	after := usbmuxNetWaitForRepair(ctx)
	// 记录修复后的状态（不确定是否完全修复，但重启已触发网络发现）。
	g.recordUsbmuxResult("ok", summarizeUsbmuxNetChange(after))
	return nil
}

// UsbmuxNetLoop 后台周期检测：只有在「开关开启 + 存在待修复设备 + 过了冷却」时才执行修复。
func (g *Gateway) UsbmuxNetLoop(ctx context.Context) {
	ticker := time.NewTicker(usbmuxNetCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !g.Cfg.UsbmuxNet.AutoRepair {
				continue
			}
			conns := usbmuxConnSetsRead()
			if !usbmuxNetNeedsRepair(conns) {
				continue
			}
			g.usbmuxMu.Lock()
			recent := time.Since(g.lastUsbmuxRepair) < usbmuxNetAutoMinInterval
			g.usbmuxMu.Unlock()
			if recent {
				continue
			}
			_ = g.usbmuxNetRepair(ctx)
		}
	}
}
