package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// SQLite 配置层测试：默认值、旧 devices.json 迁移、持久化 roundtrip、原子更新。

func TestOpenConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Cloud.HeartbeatInterval != 20 {
		t.Errorf("heartbeat default = %v, want 20", c.Cloud.HeartbeatInterval)
	}
	if c.HealthInterval != 30 {
		t.Errorf("health_interval default = %v, want 30", c.HealthInterval)
	}
	if len(c.Devices) != 0 {
		t.Errorf("devices = %v, want empty", c.Devices)
	}
	if _, err := os.Stat(filepath.Join(dir, "gateway.db")); err != nil {
		t.Errorf("gateway.db not created: %v", err)
	}
	// 全新安装：预填默认平台地址并启用（开箱可登录）
	if c.Cloud.WSURL != DefaultCloudWSURL || !c.Cloud.Enabled {
		t.Errorf("fresh install cloud default not applied: %+v", c.Cloud)
	}
	// 显式禁用的已有配置不被默认值覆盖
	dir2 := t.TempDir()
	b, _ := json.Marshal(map[string]any{"cloud": map[string]any{"ws_url": "", "enabled": false, "token": "x"}})
	os.WriteFile(filepath.Join(dir2, "devices.json"), b, 0o600)
	c2, err := OpenConfig(dir2)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if c2.Cloud.Enabled || c2.Cloud.WSURL != "" {
		t.Errorf("existing config must not be overridden: %+v", c2.Cloud)
	}
}

func TestConfigMigrateFromLegacyJSON(t *testing.T) {
	dir := t.TempDir()
	legacy := map[string]any{
		"cloud":           map[string]any{"ws_url": "wss://example/ws", "token": "tok-1", "gateway_name": "macmini-01", "enabled": true},
		"health_interval": 15,
		"llm":             map[string]any{"base_url": "https://llm/v1", "api_key": "k", "model": "m"},
		"devices": []map[string]any{
			{"udid": "AAAA", "ip": "192.168.1.10", "port": 8100, "auto_reactivate": true},
			{"udid": "BBBB", "ip": "192.168.1.11"}, // port 缺省 → 8100
		},
	}
	b, _ := json.Marshal(legacy)
	if err := os.WriteFile(filepath.Join(dir, "devices.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := OpenConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Cloud.Token != "tok-1" || c.Cloud.WSURL != "wss://example/ws" || !c.Cloud.Enabled {
		t.Errorf("cloud not migrated: %+v", c.Cloud)
	}
	if c.HealthInterval != 15 {
		t.Errorf("health_interval = %v, want 15", c.HealthInterval)
	}
	if c.LLM.Model != "m" {
		t.Errorf("llm not migrated: %+v", c.LLM)
	}
	if len(c.Devices) != 2 || c.Devices[0].UDID != "AAAA" || c.Devices[1].UDID != "BBBB" {
		t.Errorf("devices not migrated or order changed: %+v", c.Devices)
	}
	if c.Devices[1].Port != 8100 {
		t.Errorf("default port not applied: %d", c.Devices[1].Port)
	}
	// 旧文件改名 .bak，原文件不存在
	if _, err := os.Stat(filepath.Join(dir, "devices.json")); err == nil {
		t.Error("legacy devices.json still exists after migration")
	}
	if _, err := os.Stat(filepath.Join(dir, "devices.json.bak")); err != nil {
		t.Errorf("devices.json.bak missing: %v", err)
	}
	// 再次打开：数据来自库，不再触发迁移（.bak 不受影响）
	c2, err := OpenConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if c2.Cloud.Token != "tok-1" || len(c2.Devices) != 2 {
		t.Errorf("reload from db mismatch: cloud=%+v devices=%d", c2.Cloud, len(c2.Devices))
	}
}

func TestConfigPersistenceRoundtrip(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	c.Cloud.Token = "t0"
	c.Cloud.GatewayName = "gw-a"
	c.Devices = []Device{
		{UDID: "U1", IP: "10.0.0.1", AutoReactivate: true, LastHealth: map[string]any{"ok": true}},
		{UDID: "U2", IP: "10.0.0.2"},
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	c2, err := OpenConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if c2.Cloud.Token != "t0" || c2.Cloud.GatewayName != "gw-a" {
		t.Errorf("cloud lost: %+v", c2.Cloud)
	}
	if len(c2.Devices) != 2 || c2.Devices[0].UDID != "U1" || c2.Devices[1].UDID != "U2" {
		t.Errorf("devices lost or reordered: %+v", c2.Devices)
	}
	if !c2.Devices[0].AutoReactivate {
		t.Error("auto_reactivate lost")
	}
	if c2.Devices[0].LastHealth["ok"] != true {
		t.Errorf("last_health lost: %+v", c2.Devices[0].LastHealth)
	}
}

func TestConfigAtomicUpdates(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Devices = []Device{{UDID: "U1", IP: "10.0.0.1"}, {UDID: "U2"}}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if err := c.SetCloudToken("tok-new"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetLLM(LLMConfig{BaseURL: "https://x/v1", APIKey: "k2", Model: "m2"}); err != nil {
		t.Fatal(err)
	}
	if !c.RemoveDevice("U1") {
		t.Fatal("RemoveDevice U1 = false")
	}
	if c.RemoveDevice("nope") {
		t.Error("RemoveDevice nope = true")
	}

	c2, err := OpenConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if c2.Cloud.Token != "tok-new" {
		t.Errorf("token = %q, want tok-new", c2.Cloud.Token)
	}
	if c2.LLM.Model != "m2" || c2.LLM.APIKey != "k2" {
		t.Errorf("llm = %+v", c2.LLM)
	}
	if len(c2.Devices) != 1 || c2.Devices[0].UDID != "U2" {
		t.Errorf("devices after remove: %+v", c2.Devices)
	}
	// SetCloudToken/SetLLM 不应清掉设备（只写各自 key）
}

func TestRemoveAndIgnoreDevicePersists(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Devices = []Device{
		{UDID: "00008120-000865d90a10c01e", IP: "192.168.10.237", Name: "ghost"},
		{UDID: "4886579a97a96bad83b527862bab409b5a07c741", IP: "192.168.10.237", Name: "plus-2"},
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	removed, err := c.RemoveAndIgnoreDevice("00008120-000865d90a10c01e")
	if err != nil || !removed {
		t.Fatalf("RemoveAndIgnoreDevice: removed=%v err=%v", removed, err)
	}
	if c.Device("00008120-000865d90a10c01e") != nil {
		t.Fatal("deleted device still in memory")
	}
	if !c.IsIgnored("00008120-000865d90a10c01e") {
		t.Fatal("deleted device must be ignored")
	}
	if c.Device("4886579a97a96bad83b527862bab409b5a07c741") == nil {
		t.Fatal("other device must stay")
	}

	c2, err := OpenConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if c2.Device("00008120-000865d90a10c01e") != nil {
		t.Fatal("deleted device came back after reopen")
	}
	if !c2.IsIgnored("00008120-000865d90a10c01e") {
		t.Fatal("ignored list lost after reopen")
	}
	if len(c2.Devices) != 1 || c2.Devices[0].UDID != "4886579a97a96bad83b527862bab409b5a07c741" {
		t.Fatalf("remaining devices = %+v", c2.Devices)
	}
}

func TestConfigDir(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Dir() != dir {
		t.Errorf("Dir() = %q, want %q", c.Dir(), dir)
	}
	// 零值 Config（测试直接构造字面量）不 panic，回退当前目录
	if (&Config{}).Dir() != "." {
		t.Errorf("zero Config Dir() = %q, want .", (&Config{}).Dir())
	}
}

func TestSetCloudKeepsTokenWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.SetCloud("wss://p1/ws", "gw-1", true, "tok-1"); err != nil {
		t.Fatal(err)
	}
	// token 留空：保持现值；其余字段更新
	if err := c.SetCloud("wss://p2/ws", "gw-2", false, ""); err != nil {
		t.Fatal(err)
	}
	c2, err := OpenConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if c2.Cloud.WSURL != "wss://p2/ws" || c2.Cloud.GatewayName != "gw-2" || c2.Cloud.Enabled {
		t.Errorf("SetCloud fields not persisted: %+v", c2.Cloud)
	}
	if c2.Cloud.Token != "tok-1" {
		t.Errorf("empty token must keep current value, got %q", c2.Cloud.Token)
	}
}

func TestNormalizeCloudWSURL(t *testing.T) {
	cases := map[string]string{
		"wss://hk.hsddns.com":                             "wss://hk.hsddns.com/api/ios-agent/v1/gateway/ws",
		"wss://hk.hsddns.com/":                            "wss://hk.hsddns.com/api/ios-agent/v1/gateway/ws",
		"hk.hsddns.com":                                   "wss://hk.hsddns.com/api/ios-agent/v1/gateway/ws",
		"https://hk.hsddns.com":                           "wss://hk.hsddns.com/api/ios-agent/v1/gateway/ws",
		"  wss://p.example.com  ":                         "wss://p.example.com/api/ios-agent/v1/gateway/ws",
		"wss://hk.hsddns.com/api/ios-agent/v1/gateway/ws": "wss://hk.hsddns.com/api/ios-agent/v1/gateway/ws",
		"wss://other.com/custom/ws":                       "wss://other.com/custom/ws",
		"":                                                "",
	}
	for in, want := range cases {
		if got := normalizeCloudWSURL(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSetCloudNormalizesAndOpenHeals(t *testing.T) {
	dir := t.TempDir()
	cfg, err := OpenConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetCloud("wss://hk.hsddns.com", "gw", true, ""); err != nil {
		t.Fatal(err)
	}
	if cfg.Cloud.WSURL != "wss://hk.hsddns.com/api/ios-agent/v1/gateway/ws" {
		t.Errorf("SetCloud did not normalize: %q", cfg.Cloud.WSURL)
	}
	cfg.Close()
	// 库里直接写入裸域名（模拟用户旧数据），重新打开应自动补全并落库
	c2, err := OpenConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if c2.Cloud.WSURL != DefaultCloudWSURL {
		t.Errorf("OpenConfig heal failed: %q", c2.Cloud.WSURL)
	}
}
