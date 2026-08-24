package gateway

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// Gateway 聚合配置/设备/执行器/LLM/云通道的运行时状态。
type Gateway struct {
	Cfg      *Config
	WDA      *WDAManager
	Exec     *Executor
	LLM      *LLMClient
	EasyTier *EasyTierManager

	appCtx       context.Context // 顶层上下文（EnsureCloudLoop 在配置热启用后拉起云循环）
	cloudLoopRun atomic.Bool     // CloudLoop 是否在运行（未启用冷启动后可按需再拉起）

	connected   atomic.Bool
	connectedAt atomic.Value // string
	lastError   atomic.Value // string
	errAction   atomic.Bool  // lastError 是否需要人工干预（凭证类）

	identMu    sync.Mutex
	tenantID   string
	tenantName string
	userEmail  string
	userName   string

	serialMu    sync.Mutex
	serials     map[string]string    // udid -> 硬件序列号（含未配置进 devices.json 的 USB 设备）
	serialTried map[string]time.Time // 上次尝试取号时间（失败的至少间隔 10 分钟再试）

	statusMu   sync.Mutex
	lastStatus map[string]string // udid -> 上次上报云状态（online/busy/offline）

	cloudMu   sync.Mutex
	cloudConn *websocket.Conn // 当前云通道连接（RestartCloud 用）

	cloudReconnect chan struct{} // 触发 CloudLoop 立即重连（登录自动签发凭证后）
	kickWatchdog   chan struct{} // 触发看护循环立即跑一轮（激活成功后立刻发现 IP，不等 30s 周期）

	usbmuxMu         sync.Mutex // 保护 usbmux 自动修复状态
	usbmuxRepairRun  bool       // 是否正在执行一次修复（防并发）
	lastUsbmuxRepair time.Time  // 上次自动修复时间（冷却用）
	lastUsbmuxResult string     // 上次修复结果描述
}

// New 构造网关。
// SetAppContext 存入顶层上下文：页面配置热启用云通道后，EnsureCloudLoop 用它拉起循环。
func (g *Gateway) SetAppContext(ctx context.Context) { g.appCtx = ctx }

// EnsureCloudLoop 按需启动/触发云通道循环：
//   - 云通道已启用但循环未运行（如冷启动时未启用、被跳过）→ 拉起；
//   - 已在运行 → 触发立即重连（CloudLoop 每次会话都会读取最新配置）。
func (g *Gateway) EnsureCloudLoop() {
	if g.appCtx == nil {
		return
	}
	if !g.Cfg.Cloud.Enabled || g.Cfg.Cloud.WSURL == "" {
		return
	}
	if g.cloudLoopRun.CompareAndSwap(false, true) {
		go func() {
			defer g.cloudLoopRun.Store(false)
			g.CloudLoop(g.appCtx)
		}()
		return
	}
	select {
	case g.cloudReconnect <- struct{}{}:
	default:
	}
}

func New(cfg *Config, wdaMgr *WDAManager, exec *Executor, llm *LLMClient, et *EasyTierManager) *Gateway {
	return &Gateway{
		Cfg: cfg, WDA: wdaMgr, Exec: exec, LLM: llm, EasyTier: et,
		serials: map[string]string{}, serialTried: map[string]time.Time{},
		lastStatus: map[string]string{}, cloudReconnect: make(chan struct{}, 1),
		kickWatchdog: make(chan struct{}, 1),
	}
}

// KickWatchdog 非阻塞触发看护循环立即跑一轮（激活成功后立刻发现 IP）。
func (g *Gateway) KickWatchdog() {
	select {
	case g.kickWatchdog <- struct{}{}:
	default:
	}
}

// setCloudConn / clearCloudConn 记录当前云通道连接；cloudSession 在拨号成功后调用。
func (g *Gateway) setCloudConn(c *websocket.Conn) {
	g.cloudMu.Lock()
	g.cloudConn = c
	g.cloudMu.Unlock()
}

func (g *Gateway) clearCloudConn(c *websocket.Conn) {
	g.cloudMu.Lock()
	if g.cloudConn == c {
		g.cloudConn = nil
	}
	g.cloudMu.Unlock()
}

// ApplyCloudToken 保存平台签发的网关云凭证并触发重连（登录自动签发时回调）；
// tenantID 非空时一并记住所选租户（多租户账号后续轮换沿用）。
func (g *Gateway) ApplyCloudToken(token, tenantID string) error {
	if err := g.Cfg.SetCloudToken(token, tenantID); err != nil {
		return err
	}
	g.RestartCloud()
	return nil
}

// RestartCloud 关闭当前云通道连接并触发 CloudLoop 以最新配置立即重连（网关登录自动签发凭证后调用）。
func (g *Gateway) RestartCloud() {
	g.cloudMu.Lock()
	c := g.cloudConn
	g.cloudMu.Unlock()
	if c != nil {
		_ = c.Close(websocket.StatusNormalClosure, "credential rotated")
	}
	select {
	case g.cloudReconnect <- struct{}{}:
	default:
	}
}

// refreshSerials 为 USB 在线设备补取硬件序列号（ideviceinfo）。
// 成功即写入 devices.json 永久缓存；失败（未插 USB/未配对）至少 10 分钟后才重试。
func (g *Gateway) refreshSerials() {
	for _, udid := range USBUDIDs() {
		if dev := g.Cfg.Device(udid); dev != nil && dev.Serial != "" {
			g.rememberSerial(udid, dev.Serial)
			continue
		}
		g.serialMu.Lock()
		if g.serials[udid] != "" || time.Since(g.serialTried[udid]) < 10*time.Minute {
			g.serialMu.Unlock()
			continue
		}
		g.serialTried[udid] = time.Now()
		g.serialMu.Unlock()
		s := ideviceSerial(udid)
		if s == "" {
			continue
		}
		g.rememberSerial(udid, s)
		if dev := g.Cfg.Device(udid); dev != nil && dev.Serial == "" {
			dev.Serial = s
			_ = g.Cfg.Save()
		}
	}
}

// refreshIOSVersions 给 USB 在线设备补 ProductVersion（激活分流、管理页展示用）。
func (g *Gateway) refreshIOSVersions() {
	if g == nil || g.Cfg == nil {
		return
	}
	dirty := false
	seen := map[string]bool{}
	for _, u := range USBUDIDs() {
		seen[u] = true
		if v := cachedIOSVersion(u); v != "" {
			if dev := g.Cfg.Device(u); dev != nil && dev.IOSVersion == "" {
				dev.IOSVersion = v
				dirty = true
			}
			continue
		}
		if v := strings.TrimSpace(ideviceInfoValue(u, "ProductVersion")); v != "" {
			rememberIOSVersion(u, v)
			if dev := g.Cfg.Device(u); dev != nil && dev.IOSVersion != v {
				dev.IOSVersion = v
				dirty = true
			}
		}
	}
	for i := range g.Cfg.Devices {
		dev := &g.Cfg.Devices[i]
		if dev.UDID == "" || !seen[dev.UDID] || dev.IOSVersion != "" {
			continue
		}
		if v := cachedIOSVersion(dev.UDID); v != "" {
			dev.IOSVersion = v
			dirty = true
		}
	}
	if dirty {
		_ = g.Cfg.Save()
	}
}

func (g *Gateway) rememberSerial(udid, serial string) {
	g.serialMu.Lock()
	g.serials[udid] = serial
	g.serialMu.Unlock()
}

// SerialOf 返回设备序列号（devices.json 缓存优先，内存缓存兜底）。
func (g *Gateway) SerialOf(udid string) string {
	if dev := g.Cfg.Device(udid); dev != nil && dev.Serial != "" {
		return dev.Serial
	}
	g.serialMu.Lock()
	defer g.serialMu.Unlock()
	return g.serials[udid]
}

// SetConnected 更新云通道连接状态。
func (g *Gateway) SetConnected(v bool) {
	g.connected.Store(v)
	if v {
		g.connectedAt.Store(time.Now().Format("2006-01-02 15:04:05"))
	}
}

// Connected 当前是否连接。
func (g *Gateway) Connected() bool { return g.connected.Load() }

// ConnectedAt 首次连接时间。
func (g *Gateway) ConnectedAt() string {
	if v := g.connectedAt.Load(); v != nil {
		return v.(string)
	}
	return ""
}

// SetIdentity 记录登录后平台回执的租户/用户身份。
func (g *Gateway) SetIdentity(tenantID, tenantName, userEmail, userName string) {
	g.identMu.Lock()
	defer g.identMu.Unlock()
	g.tenantID, g.tenantName, g.userEmail, g.userName = tenantID, tenantName, userEmail, userName
}

// Identity 返回登录后解析出的租户/用户身份。
func (g *Gateway) Identity() (tenantID, tenantName, userEmail, userName string) {
	g.identMu.Lock()
	defer g.identMu.Unlock()
	return g.tenantID, g.tenantName, g.userEmail, g.userName
}

// SetLastError 记录最近一次云错误；actionable 表示需要人工干预（如凭证失效），
// 瞬时错误（平台重启/网络抖动，网关会自动恢复）传 false。
func (g *Gateway) SetLastError(s string, actionable bool) {
	g.lastError.Store(s)
	g.errAction.Store(actionable)
}

// LastError 最近一次云错误。
func (g *Gateway) LastError() string {
	if v := g.lastError.Load(); v != nil {
		return v.(string)
	}
	return ""
}

// LastErrorActionable 最近一次云错误是否需要人工干预。
func (g *Gateway) LastErrorActionable() bool { return g.errAction.Load() }

// ApplyLLMConfig 应用平台下发的视觉/LLM 模型配置：落盘并热替换运行时客户端。
func (g *Gateway) ApplyLLMConfig(cfg LLMConfig) error {
	if err := g.Cfg.SetLLM(cfg); err != nil {
		return err
	}
	llm := NewLLMClient(cfg.BaseURL, cfg.APIKey, cfg.Model)
	g.LLM = llm
	g.Exec.SetLLM(llm)
	return nil
}
