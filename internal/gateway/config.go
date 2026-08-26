package gateway

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动（无 CGO）
)

// 配置与设备持久化（SQLite，<state>/gateway.db）：
//   meta(key,value)    schema 版本
//   config(key,value)  cloud/llm/web/health_interval/signing，value 为 JSON
//   devices(udid,data) 每台一行 JSON（含 last_health 等运行时缓存状态）
// 兼容迁移：state 目录存在旧 devices.json 且库为空时一次性导入，旧文件改名 .bak。
// 所有持久化入口收敛在本文件（Save/SetCloudToken/SetLLM/RemoveDevice）；
// Config 结构体与方法签名即对外接口，调用方不感知存储介质。

// Config 是网关配置与设备表的运行时结构（持久化在 gateway.db）。
type Config struct {
	mu             sync.Mutex
	db             *sql.DB
	dir            string          // state 目录（gateway.db、data/ 的锚点）
	Cloud          CloudConfig     `json:"cloud"`
	Devices        []Device        `json:"devices"`
	HealthInterval float64         `json:"health_interval"`
	LLM            LLMConfig       `json:"llm,omitempty"`
	Autonomy       AutonomyConfig  `json:"autonomy,omitempty"`
	Web            WebConfig       `json:"web,omitempty"`
	Signing        SigningConfig   `json:"signing,omitempty"`
	UsbmuxNet      UsbmuxNetConfig `json:"usbmux_net,omitempty"`
}

// SigningConfig 构建/签名配置（打包交付用）。
type SigningConfig struct {
	// Team 透传给 xcodebuild 的 DEVELOPMENT_TEAM；空=工程内写死值（现状）。
	Team string `json:"team,omitempty"`
	// Activator 激活后端：auto（默认）| xcodebuild | goios | tidevice。
	// auto：有 ios/tidevice 就走 IPA 安装+拉起；都没有时 Windows 仍报 goios，Mac 才回退 xcodebuild。
	Activator string `json:"activator,omitempty"`
	// WDABundleID 已安装到手机上的 WDA Runner bundle id，供 goios/tidevice 拉起 XCTest。
	// 空则用工程默认 com.wda.WebRunner.xctrunner。
	WDABundleID string `json:"wda_bundle_id,omitempty"`
	// IPAPath 已签名的 WDA Runner IPA。激活时若手机还没装 Runner，会用 tidevice/go-ios 安装这份包。
	// 空则由启动参数落到 <state>/wda.ipa。
	IPAPath string `json:"ipa_path,omitempty"`
}

// WebConfig 管理页与发送行为配置。
type WebConfig struct {
	// CookieSecure 会话 cookie 是否加 Secure（经 HTTPS 反代暴露时打开；本地 HTTP 默认关闭）。
	CookieSecure bool `json:"cookie_secure,omitempty"`
	// SendTimezone 发送时间窗使用的 IANA 时区名（如 Asia/Shanghai）；空=网关本机时区。
	SendTimezone string `json:"send_timezone,omitempty"`
	// ChatListMaxFriends 未指定号码时，聊天列表最多发送多少个 1:1 好友。
	// 0 或负数按默认 30；超过硬顶 100 按 100，避免名单过长转圈发不完。
	ChatListMaxFriends int `json:"chat_list_max_friends,omitempty"`
	// ChatListRepeatDays 联系人重复触达去重窗口（天）：同一设备发给同一联系人后，
	// 窗口内再次按聊天列表好友群发会跳过该联系人；<=0 按默认 3 天。
	ChatListRepeatDays int `json:"chat_list_repeat_days,omitempty"`
}

// UsbmuxNetConfig usbmux 无线调试（ConnectionType=Network）自动修复设置。
type UsbmuxNetConfig struct {
	// AutoRepair 开启后，后台周期检测「USB 已接入但缺 usbmux Network 条目」的设备，
	// 满足条件时自动重启系统 usbmuxd 触发网络发现，并校验 Network 条目重新出现。
	AutoRepair bool `json:"auto_repair"`
}

// CloudConfig 云通道（平台网关 WSS）。
type CloudConfig struct {
	WSURL             string  `json:"ws_url"`
	Token             string  `json:"token"`
	GatewayName       string  `json:"gateway_name"`
	Enabled           bool    `json:"enabled"`
	HeartbeatInterval float64 `json:"heartbeat_interval"`
	// TenantID 多租户账号（如平台超管）自动签发凭证时指定的租户；
	// 平台 422 TENANT_AMBIGUOUS 后由用户在网关页面选择并保存。
	TenantID string `json:"tenant_id,omitempty"`
}

// Device 单台手机。
type Device struct {
	UDID           string `json:"udid"`
	Serial         string `json:"serial,omitempty"` // 硬件序列号（USB 时经 ideviceinfo 取得，插一次即永久缓存）
	IP             string `json:"ip"`
	Port           int    `json:"port"`
	AutoReactivate bool   `json:"auto_reactivate"`
	// ActivateVia 上次手动选择的激活通道：usb 或 network。自动重激活沿用此值。
	ActivateVia string `json:"activate_via,omitempty"`
	// WifiDebug 首次授权已写入 EnableWifiConnections + EnableWifiDebugging。不含锁屏密码。
	WifiDebug        bool           `json:"wifi_debug,omitempty"`
	VendorUUID       string         `json:"vendor_uuid,omitempty"`
	IOSVersion       string         `json:"ios_version,omitempty"`
	Name             string         `json:"name,omitempty"`
	Model            string         `json:"model,omitempty"`
	WhatsAppBundleID string         `json:"whatsapp_bundle_id,omitempty"`
	LastHealth       map[string]any `json:"last_health,omitempty"`
}

// LLMConfig 视觉/LLM 兜底配置（OpenAI-compatible）。
type LLMConfig struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
}

// AutonomyConfig 网关自主群发 Agent（本地任务）配置。
// 目标是 WhatsApp 应用联系人（聊天列表 1:1 好友），不需要外部号码列表。
type AutonomyConfig struct {
	// Enabled 总开关，默认 false（opt-in）。
	Enabled bool `json:"enabled"`
	// Content 话术模板；空则无法自主发送（“没有群发不触发事件”）。
	Content string `json:"content"`
	// MaxFriends 单个自主任务对应用联系人的发送上限（0=沿用 WebConfig.ChatListMaxFriends）。
	MaxFriends int `json:"max_friends,omitempty"`
	// WindowStart/WindowEnd 发送窗口（HH:MM，空=不限制）。
	WindowStart string `json:"window_start,omitempty"`
	WindowEnd   string `json:"window_end,omitempty"`
	// IntervalSec 条间节奏（默认 20）；BurstCount/BurstPauseSec 爆点（默认 5/60）。
	IntervalSec   int `json:"interval_sec,omitempty"`
	BurstCount    int `json:"burst_count,omitempty"`
	BurstPauseSec int `json:"burst_pause_sec,omitempty"`
	// DailyCap 每日自主发送上限（默认 40）。
	DailyCap int `json:"daily_cap,omitempty"`
	// MaxNewSessionRatio 当日新会话占比上限（默认 30）。
	MaxNewSessionRatio int `json:"max_new_session_ratio,omitempty"`
	// TickInterval 自主回路周期（秒，默认 60）。
	TickInterval int `json:"tick_interval,omitempty"`
}

// UnmarshalJSON 同时接受 snake_case 与平台可能下发的 camelCase。
func (c *LLMConfig) UnmarshalJSON(b []byte) error {
	type plain LLMConfig
	var a struct {
		plain
		BaseURLCamel string `json:"baseUrl"`
		APIKeyCamel  string `json:"apiKey"`
	}
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*c = LLMConfig(a.plain)
	if c.BaseURL == "" {
		c.BaseURL = a.BaseURLCamel
	}
	if c.APIKey == "" {
		c.APIKey = a.APIKeyCamel
	}
	return nil
}

const configSchemaVersion = "1"

// DefaultCloudWSURL 产品默认云平台地址：全新安装（库内无任何 cloud 配置）时预填，
// 让客户开箱即可登录（登录成功自动签发网关凭证）；已有配置（含迁移导入）不覆盖。
const DefaultCloudWSURL = "wss://hk.hsddns.com/api/ios-agent/v1/gateway/ws"

// defaultCloudWSPath 平台网关 WS 的标准路径（用于裸域名输入的自动补全）。
const defaultCloudWSPath = "/api/ios-agent/v1/gateway/ws"

// normalizeCloudWSURL 容错归一化用户输入的平台地址：
//   - https→wss、http→ws；无协议前缀的裸域名补 wss://；
//   - 路径为空或 "/" 时补标准网关路径（用户常填 wss://hk.hsddns.com 裸域名）。
func normalizeCloudWSURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return u
	}
	if !strings.Contains(u, "://") {
		u = "wss://" + u
	}
	if strings.HasPrefix(u, "https://") {
		u = "wss://" + strings.TrimPrefix(u, "https://")
	} else if strings.HasPrefix(u, "http://") {
		u = "ws://" + strings.TrimPrefix(u, "http://")
	}
	if parsed, err := url.Parse(u); err == nil && parsed.Host != "" {
		if parsed.Path == "" || parsed.Path == "/" {
			parsed.Path = defaultCloudWSPath
			u = parsed.String()
		}
	}
	return u
}

// OpenConfig 打开（必要时创建）<state>/gateway.db 并加载配置。
// state 目录存在旧 devices.json 且库为空时执行一次性迁移（旧文件改名 .bak）。
func OpenConfig(stateDir string) (*Config, error) {
	if stateDir == "" {
		stateDir = "."
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(stateDir, "gateway.db")+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS config (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS devices (
	udid TEXT PRIMARY KEY,
	data TEXT NOT NULL
);`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init gateway.db: %w", err)
	}
	var ver string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&ver); err == sql.ErrNoRows {
		if _, err := db.Exec(`INSERT INTO meta(key,value) VALUES('schema_version',?)`, configSchemaVersion); err != nil {
			_ = db.Close()
			return nil, err
		}
	} else if err != nil {
		_ = db.Close()
		return nil, err
	} else if ver != configSchemaVersion {
		_ = db.Close()
		return nil, fmt.Errorf("gateway.db schema version %q unsupported (want %q)", ver, configSchemaVersion)
	}

	c := &Config{db: db, dir: stateDir, HealthInterval: 30}
	if err := c.migrateFromJSON(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := c.load(); err != nil {
		_ = db.Close()
		return nil, err
	}
	// 旧版「隐藏/恢复」列表已废弃：掉线或手动删除均为物理删除，避免垃圾 UDID 冲突。
	_, _ = db.Exec(`DELETE FROM config WHERE key='ignored'`)
	// 全新安装：库内无任何 cloud 配置时预填默认平台地址并启用，
	// 客户开箱即可登录（页面无 cloud 配置编辑入口，源码形态靠手改文件）。
	if c.Cloud.WSURL == "" && c.Cloud.Token == "" {
		c.Cloud.WSURL = DefaultCloudWSURL
		c.Cloud.Enabled = true
	}
	// 已有地址容错归一化（如保存过裸域名 wss://host，补标准网关路径）
	if normalized := normalizeCloudWSURL(c.Cloud.WSURL); normalized != c.Cloud.WSURL {
		c.Cloud.WSURL = normalized
		_ = c.writeKey("cloud", c.Cloud)
	}
	if c.Cloud.HeartbeatInterval <= 0 {
		c.Cloud.HeartbeatInterval = 20
	}
	if c.HealthInterval <= 0 {
		c.HealthInterval = 30
	}
	if c.Web.ChatListRepeatDays <= 0 {
		c.Web.ChatListRepeatDays = 3
	}
	for i := range c.Devices {
		if c.Devices[i].Port == 0 {
			c.Devices[i].Port = 8100
		}
	}
	return c, nil
}

// Close 关闭底层库（进程退出时调用，WAL 正常 checkpoint）。
func (c *Config) Close() error { return c.db.Close() }

// migrateFromJSON 旧 devices.json 一次性迁移：库为空（config+devices 均无行）且
// <state>/devices.json 存在时导入并改名 .bak；否则不动。
func (c *Config) migrateFromJSON() error {
	legacy := filepath.Join(c.dir, "devices.json")
	if _, err := os.Stat(legacy); err != nil {
		return nil
	}
	var cfgRows, devRows int
	if err := c.db.QueryRow(`SELECT count(*) FROM config`).Scan(&cfgRows); err != nil {
		return err
	}
	if err := c.db.QueryRow(`SELECT count(*) FROM devices`).Scan(&devRows); err != nil {
		return err
	}
	if cfgRows > 0 || devRows > 0 {
		return nil // 已有数据，不迁移
	}
	b, err := os.ReadFile(legacy)
	if err != nil {
		return nil // 读不了就当没有，库从默认值开始
	}
	old := &Config{HealthInterval: 30}
	if err := json.Unmarshal(b, old); err != nil {
		return fmt.Errorf("migrate %s: %w", legacy, err)
	}
	c.Cloud, c.Devices, c.HealthInterval = old.Cloud, old.Devices, old.HealthInterval
	c.LLM, c.Web = old.LLM, old.Web
	if err := c.saveLocked(); err != nil {
		return fmt.Errorf("migrate write db: %w", err)
	}
	return os.Rename(legacy, legacy+".bak")
}

// load 从库填充内存结构（devices 按插入序，与旧 JSON 数组序一致）。
func (c *Config) load() error {
	if v, ok, err := c.readKey("cloud"); err != nil {
		return err
	} else if ok {
		if err := json.Unmarshal([]byte(v), &c.Cloud); err != nil {
			return fmt.Errorf("config.cloud: %w", err)
		}
	}
	if v, ok, err := c.readKey("llm"); err != nil {
		return err
	} else if ok {
		if err := json.Unmarshal([]byte(v), &c.LLM); err != nil {
			return fmt.Errorf("config.llm: %w", err)
		}
	}
	if v, ok, err := c.readKey("autonomy"); err != nil {
		return err
	} else if ok {
		if err := json.Unmarshal([]byte(v), &c.Autonomy); err != nil {
			return fmt.Errorf("config.autonomy: %w", err)
		}
	}
	if v, ok, err := c.readKey("web"); err != nil {
		return err
	} else if ok {
		if err := json.Unmarshal([]byte(v), &c.Web); err != nil {
			return fmt.Errorf("config.web: %w", err)
		}
	}
	if v, ok, err := c.readKey("signing"); err != nil {
		return err
	} else if ok {
		if err := json.Unmarshal([]byte(v), &c.Signing); err != nil {
			return fmt.Errorf("config.signing: %w", err)
		}
	}
	if v, ok, err := c.readKey("usbmux_net"); err != nil {
		return err
	} else if ok {
		if err := json.Unmarshal([]byte(v), &c.UsbmuxNet); err != nil {
			return fmt.Errorf("config.usbmux_net: %w", err)
		}
	}
	if v, ok, err := c.readKey("health_interval"); err != nil {
		return err
	} else if ok {
		if err := json.Unmarshal([]byte(v), &c.HealthInterval); err != nil {
			return fmt.Errorf("config.health_interval: %w", err)
		}
	}
	rows, err := c.db.Query(`SELECT data FROM devices ORDER BY rowid`)
	if err != nil {
		return err
	}
	defer rows.Close()
	c.Devices = nil
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return err
		}
		var d Device
		if err := json.Unmarshal([]byte(data), &d); err != nil {
			return fmt.Errorf("devices row: %w", err)
		}
		c.Devices = append(c.Devices, d)
	}
	return rows.Err()
}

func (c *Config) readKey(key string) (value string, ok bool, err error) {
	err = c.db.QueryRow(`SELECT value FROM config WHERE key=?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func (c *Config) writeKey(key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = c.db.Exec(`INSERT INTO config(key,value) VALUES(?,?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, string(b))
	return err
}

// saveLocked 全量落库（调用方需持有 mu）。
func (c *Config) saveLocked() error {
	if c == nil || c.db == nil {
		return errors.New("config db not open")
	}
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	upsert := func(key string, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO config(key,value) VALUES(?,?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, string(b)); err != nil {
			return err
		}
		return nil
	}
	for key, v := range map[string]any{
		"cloud":           c.Cloud,
		"llm":             c.LLM,
		"autonomy":        c.Autonomy,
		"web":             c.Web,
		"signing":         c.Signing,
		"health_interval": c.HealthInterval,
	} {
		if err := upsert(key, v); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM devices`); err != nil {
		return err
	}
	for _, d := range c.Devices {
		b, err := json.Marshal(d)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO devices(udid,data) VALUES(?,?)`, d.UDID, string(b)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Save 全量写回库（加锁，避免并发写坏）。
func (c *Config) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saveLocked()
}

// Dir 返回状态目录（网关数据根目录，data/、gateway.db 位于其下）。
func (c *Config) Dir() string {
	if c.dir == "" {
		return "."
	}
	return c.dir
}

// Device 按 UDID 查找设备。
func (c *Config) Device(udid string) *Device {
	for i := range c.Devices {
		if c.Devices[i].UDID == udid {
			return &c.Devices[i]
		}
	}
	return nil
}

// RemoveDevice 从配置物理删除设备并落盘；返回设备是否原本存在。
// 不隐藏、不保留恢复入口。USB 仍连接时，下一轮发现会以未配置设备重新出现。
func (c *Config) RemoveDevice(udid string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	found := false
	for i := range c.Devices {
		if c.Devices[i].UDID == udid {
			c.Devices = append(c.Devices[:i], c.Devices[i+1:]...)
			found = true
			break
		}
	}
	if found && c.db != nil {
		_, _ = c.db.Exec(`DELETE FROM devices WHERE udid=?`, udid)
	}
	return found
}

// SetCloudToken 原子更新网关云凭证并落库（网关登录后自动签发时调用）；
// tenantID 非空时一并更新（多租户账号签发成功后记住所选租户，后续轮换沿用）。
func (c *Config) SetCloudToken(token string, tenantID ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Cloud.Token = token
	if len(tenantID) > 0 && tenantID[0] != "" {
		c.Cloud.TenantID = tenantID[0]
	}
	return c.writeKey("cloud", c.Cloud)
}

// SetCloud 更新云通道连接设置（页面「云通道设置」）并落库。
// token 非空才更新（留空保持现值——网关凭证通常由平台登录后自动签发，不手工轮换）。
func (c *Config) SetCloud(wsURL, gatewayName string, enabled bool, token string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Cloud.WSURL = normalizeCloudWSURL(wsURL)
	c.Cloud.GatewayName = gatewayName
	c.Cloud.Enabled = enabled
	if token != "" {
		c.Cloud.Token = token
	}
	return c.writeKey("cloud", c.Cloud)
}

// SetLLM 原子更新视觉/LLM 配置并落库（供平台下发 model:config 调用）。
func (c *Config) SetLLM(cfg LLMConfig) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.LLM = cfg
	return c.writeKey("llm", c.LLM)
}

// SetAutonomy 原子更新自主群发配置并落库（供管理页 /api/autonomy 调用）。
func (c *Config) SetAutonomy(cfg AutonomyConfig) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Autonomy = cfg
	return c.writeKey("autonomy", c.Autonomy)
}

// SetChatListRepeatDays 原子更新联系人重复去重窗口（天）并落库。
func (c *Config) SetChatListRepeatDays(days int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if days <= 0 {
		days = 3
	}
	c.Web.ChatListRepeatDays = days
	return c.writeKey("web", c.Web)
}

// SetUsbmuxNet 保存「usbmux Network 自动修复」开关，立即持久化。
func (c *Config) SetUsbmuxNet(autoRepair bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.UsbmuxNet.AutoRepair = autoRepair
	return c.writeKey("usbmux_net", c.UsbmuxNet)
}

// ReadExtra 读取附加配置 kv（如 easytier），value 为 JSON 文本；不存在返回 ok=false。
func (c *Config) ReadExtra(key string) (string, bool) {
	v, ok, err := c.readKey(key)
	if err != nil {
		return "", false
	}
	return v, ok
}

// WriteExtra 写入附加配置 kv（与 ReadExtra 对应，落 config 表；value 原样存储，不做 JSON 包裹）。
func (c *Config) WriteExtra(key, value string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.db.Exec(`INSERT INTO config(key,value) VALUES(?,?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}
