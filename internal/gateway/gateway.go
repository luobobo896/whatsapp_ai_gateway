package gateway

import (
	"sync"
	"sync/atomic"
	"time"
)

// Gateway 聚合配置/设备/执行器/LLM/云通道的运行时状态。
type Gateway struct {
	Cfg      *Config
	WDA      *WDAManager
	Exec     *Executor
	LLM      *LLMClient
	EasyTier *EasyTierManager

	connected   atomic.Bool
	connectedAt atomic.Value // string
	lastError   atomic.Value // string

	identMu    sync.Mutex
	tenantID   string
	tenantName string
	userEmail  string
	userName   string
}

// New 构造网关。
func New(cfg *Config, wdaMgr *WDAManager, exec *Executor, llm *LLMClient, et *EasyTierManager) *Gateway {
	return &Gateway{Cfg: cfg, WDA: wdaMgr, Exec: exec, LLM: llm, EasyTier: et}
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

// SetLastError 记录最近一次云错误。
func (g *Gateway) SetLastError(s string) { g.lastError.Store(s) }

// LastError 最近一次云错误。
func (g *Gateway) LastError() string {
	if v := g.lastError.Load(); v != nil {
		return v.(string)
	}
	return ""
}
