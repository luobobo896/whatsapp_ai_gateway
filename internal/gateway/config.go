package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Config 是网关本地配置（devices.json）的运行时结构。
type Config struct {
	mu             sync.Mutex
	path           string
	Cloud          CloudConfig `json:"cloud"`
	Devices        []Device    `json:"devices"`
	HealthInterval float64     `json:"health_interval"`
	LLM            LLMConfig   `json:"llm,omitempty"`
	Web            WebConfig   `json:"web,omitempty"`
}

// WebConfig 管理页与发送行为配置。
type WebConfig struct {
	// CookieSecure 会话 cookie 是否加 Secure（经 HTTPS 反代暴露时打开；本地 HTTP 默认关闭）。
	CookieSecure bool `json:"cookie_secure,omitempty"`
	// SendTimezone 发送时间窗使用的 IANA 时区名（如 Asia/Shanghai）；空=网关本机时区。
	SendTimezone string `json:"send_timezone,omitempty"`
}

// CloudConfig 云通道（平台网关 WSS）。
type CloudConfig struct {
	WSURL             string  `json:"ws_url"`
	Token             string  `json:"token"`
	GatewayName       string  `json:"gateway_name"`
	Enabled           bool    `json:"enabled"`
	HeartbeatInterval float64 `json:"heartbeat_interval"`
}

// Device 单台手机。
type Device struct {
	UDID             string         `json:"udid"`
	Serial           string         `json:"serial,omitempty"` // 硬件序列号（USB 时经 ideviceinfo 取得，插一次即永久缓存）
	IP               string         `json:"ip"`
	Port             int            `json:"port"`
	AutoReactivate   bool           `json:"auto_reactivate"`
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

// LoadConfig 读取配置文件；不存在时返回默认配置。
func LoadConfig(path string) (*Config, error) {
	if path == "" {
		path = DefaultConfigPath()
	}
	c := &Config{path: path, HealthInterval: 30}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, c); err != nil {
			return nil, err
		}
	}
	if c.Cloud.HeartbeatInterval <= 0 {
		c.Cloud.HeartbeatInterval = 20
	}
	if c.HealthInterval <= 0 {
		c.HealthInterval = 30
	}
	for i := range c.Devices {
		if c.Devices[i].Port == 0 {
			c.Devices[i].Port = 8100
		}
	}
	return c, nil
}

// DefaultConfigPath 默认 devices.json 路径（与 Python 网关同级）。
func DefaultConfigPath() string {
	if p := os.Getenv("GATEWAY_CONFIG"); p != "" {
		return p
	}
	// cmd/gateway 运行时，默认取当前目录 devices.json；也可用 GATEWAY_CONFIG 覆盖。
	return filepath.Join(".", "devices.json")
}

// Save 写回配置（加锁，避免并发写坏）。
func (c *Config) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.path, b, 0o600)
}

// Dir 返回配置文件所在目录（网关数据根目录，data/ 位于其下）。
func (c *Config) Dir() string {
	return filepath.Dir(c.path)
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

// RemoveDevice 从配置移除设备并落盘；返回设备是否原本存在。
// USB 仍连接的设备删除后会以「未配置」身份重新出现在列表（发现层自动恢复，防误删）。
func (c *Config) RemoveDevice(udid string) bool {
	for i := range c.Devices {
		if c.Devices[i].UDID == udid {
			c.Devices = append(c.Devices[:i], c.Devices[i+1:]...)
			_ = c.Save()
			return true
		}
	}
	return false
}

// SetCloudToken 原子更新网关云凭证并落盘（网关登录后自动签发时调用）。
func (c *Config) SetCloudToken(token string) error {
	c.mu.Lock()
	c.Cloud.Token = token
	b, err := json.MarshalIndent(c, "", "  ")
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return os.WriteFile(c.path, b, 0o600)
}

// SetLLM 原子更新视觉/LLM 配置并落盘（供平台下发 model:config 调用）。
func (c *Config) SetLLM(cfg LLMConfig) error {
	c.mu.Lock()
	c.LLM = cfg
	b, err := json.MarshalIndent(c, "", "  ")
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return os.WriteFile(c.path, b, 0o600)
}
