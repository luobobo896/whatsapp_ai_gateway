package gateway

import (
	"os"
	"path/filepath"
	"testing"
)

// easytier 配置 SQLite 化：保存/重开 roundtrip、旧 data/easytier.json 一次性迁移。

func TestEasyTierConfigSQLiteRoundtrip(t *testing.T) {
	dir := t.TempDir()
	cfg, err := OpenConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer cfg.Close()
	m := NewEasyTierManager(dir, cfg)
	// 全新状态：业务拓扑字段为空（等待平台下发）
	if m.config.NetworkName != "" || m.config.RelayHost != "" || m.config.GatewayIPv4 != "" {
		t.Errorf("fresh config must be empty, got %+v", m.config)
	}
	m.config.NetworkName = "net-1"
	m.config.NetworkSecret = "sec-1"
	m.config.RelayHost = "relay.example.com"
	if err := m.save(); err != nil {
		t.Fatal(err)
	}
	m2 := NewEasyTierManager(dir, cfg)
	if m2.config.NetworkName != "net-1" || m2.config.NetworkSecret != "sec-1" || m2.config.RelayHost != "relay.example.com" {
		t.Errorf("config lost after reopen: %+v", m2.config)
	}
}

func TestEasyTierConfigMigrateFromLegacyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"network_name":"legacy-net","relay_host":"legacy.example.com","relay_port":1234,"network_secret":"s","gateway_ipv4":"10.1.1.9","mtu":1400,"sudo":true}`
	if err := os.WriteFile(filepath.Join(dir, "data", "easytier.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := OpenConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer cfg.Close()
	m := NewEasyTierManager(dir, cfg)
	if m.config.NetworkName != "legacy-net" || m.config.RelayHost != "legacy.example.com" || m.config.RelayPort != 1234 {
		t.Errorf("legacy config not migrated: %+v", m.config)
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "easytier.json")); err == nil {
		t.Error("legacy easytier.json still exists after migration")
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "easytier.json.bak")); err != nil {
		t.Errorf("easytier.json.bak missing: %v", err)
	}
	// 重开走库，不再依赖文件
	m2 := NewEasyTierManager(dir, cfg)
	if m2.config.NetworkName != "legacy-net" {
		t.Errorf("reload from db mismatch: %+v", m2.config)
	}
}
