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

// WebConfig Web 管理页配置（登录鉴权）。
type WebConfig struct {
	// Username 管理页登录用户名（与平台账号一致，默认 admin@whatsapp-ai.local）。
	Username string `json:"username,omitempty"`
	// Password 管理页登录密码。为空表示不要求登录（局域网开放模式，UI 会提示风险）。
	// 设置后所有 /api/* 需 session cookie（登录后签发，12 小时有效）。
	Password string `json:"password"`
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
	// 与平台账号保持一致：设置了密码但没写用户名时，默认用平台管理员账号。
	if c.Web.Password != "" && c.Web.Username == "" {
		c.Web.Username = "admin@whatsapp-ai.local"
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

// Device 按 UDID 查找设备。
func (c *Config) Device(udid string) *Device {
	for i := range c.Devices {
		if c.Devices[i].UDID == udid {
			return &c.Devices[i]
		}
	}
	return nil
}
