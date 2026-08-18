package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// EasyTierConfig 是 easytier 节点配置（与平台下发的 easytier:config 对齐）。
type EasyTierConfig struct {
	NetworkName   string `json:"network_name"`
	NetworkSecret string `json:"network_secret"`
	RelayHost     string `json:"relay_host"`
	RelayPort     int    `json:"relay_port"`
	NetworkCIDR   string `json:"network_cidr"`
	GatewayIPv4   string `json:"gateway_ipv4"`
	MTU           int    `json:"mtu"`
	Sudo          bool   `json:"sudo"`
}

func defaultEasyTierConfig() EasyTierConfig {
	// 业务拓扑字段（network_name/relay_host/network_cidr/gateway_ipv4）默认留空：
	// 全新安装显示「未配置」，等待平台 easytier:config 下发（Apply 按非空字段合并）。
	// 仅保留端口/MTU/模式等技术默认值。
	return EasyTierConfig{
		NetworkName: "", RelayHost: "", RelayPort: 11010,
		NetworkCIDR: "", GatewayIPv4: "", MTU: 1380, Sudo: true,
	}
}

type easyTierProc struct {
	cmd  *exec.Cmd
	done chan struct{}
}

// EasyTierManager 集成 easytier 服务（可选后备通道，默认关闭）。
type EasyTierManager struct {
	root       string
	cfg        *Config
	configPath string
	tomlPath   string
	logPath    string
	rpcPortal  string
	setupSudo  string

	mu           sync.Mutex
	process      *easyTierProc
	config       EasyTierConfig
	desired      bool      // 用户意图：true=应运行（崩溃自动拉回），false=已主动停止
	lastRestart  time.Time // 上次自愈重启时刻（限速，防崩溃风暴）
	startMu      sync.Mutex
}

// systemEasyTierDir 打包交付形态的固定安装路径：sudoers 放行该绝对路径，
// 不随 app 更新/移动失效（bundle 内只是安装源，安装脚本拷贝到此处）。
const systemEasyTierDir = "/usr/local/libexec/wda-gateway"

// bundleResourcesDir 壳 App 注入的 bundle Resources 路径（打包交付形态；
// 源码运行时为空，全部回退到 <state> 相对布局）。
func bundleResourcesDir() string { return os.Getenv("WDA_GATEWAY_RESOURCES") }

// easytierBinDir 二进制目录查找：优先系统固定安装路径（若已安装），
// 其次仓库内 tools/easytier（源码运行形态）。与 setup-easytier-sudo.sh 的放行路径保持一致。
func easytierBinDir(root string) string {
	if _, err := os.Stat(filepath.Join(systemEasyTierDir, "easytier-core")); err == nil {
		return systemEasyTierDir
	}
	if res := bundleResourcesDir(); res != "" {
		if p := filepath.Join(res, "tools", "easytier"); fileExists(filepath.Join(p, "easytier-core")) {
			return p
		}
	}
	return filepath.Join(root, "tools", "easytier")
}

// setupSudoScript 授权脚本路径：优先 bundle Resources（壳注入），其次 <state>/scripts（源码形态）。
func setupSudoScript(root string) string {
	if res := bundleResourcesDir(); res != "" {
		if p := filepath.Join(res, "scripts", "setup-easytier-sudo.sh"); fileExists(p) {
			return p
		}
	}
	return filepath.Join(root, "scripts", "setup-easytier-sudo.sh")
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// corePath/cliPath 每次解析二进制路径：授权安装到系统固定路径后立即生效，
// 不依赖构造时刻的目录状态。
func (m *EasyTierManager) corePath() string { return filepath.Join(easytierBinDir(m.root), "easytier-core") }
func (m *EasyTierManager) cliPath() string  { return filepath.Join(easytierBinDir(m.root), "easytier-cli") }

// NewEasyTierManager 构造管理器；root 为网关状态目录（tools/easytier、data、scripts 位于其下）。
func NewEasyTierManager(root string, cfg *Config) *EasyTierManager {
	m := &EasyTierManager{
		root:       root,
		cfg:        cfg,
		configPath: filepath.Join(root, "data", "easytier.json"), // 仅作旧文件迁移判定，运行时读写走 gateway.db
		tomlPath:   "/tmp/wda-gateway-easytier.toml",             // easytier-core 运行时输入（外部二进制消费，与 wda 日志同策略放 /tmp）
		logPath:    "/tmp/easytier-gateway.log",
		rpcPortal:  "127.0.0.1:15888",
		setupSudo:  setupSudoScript(root),
	}
	m.load()
	return m
}

// ---- 配置 ----

// load 从 gateway.db（config 表 easytier key）加载；旧 data/easytier.json 存在且库内
// 无记录时一次性迁移（旧文件改名 .bak），与 devices.json 迁移同策略。
func (m *EasyTierManager) load() {
	m.config = defaultEasyTierConfig()
	if v, ok := m.cfg.ReadExtra("easytier"); ok {
		_ = json.Unmarshal([]byte(v), &m.config)
		return
	}
	if b, err := os.ReadFile(m.configPath); err == nil {
		var old EasyTierConfig
		if json.Unmarshal(b, &old) == nil {
			m.config = old
			_ = m.save() // 导入库
			_ = os.Rename(m.configPath, m.configPath+".bak")
		}
	}
}

func (m *EasyTierManager) save() error {
	b, _ := json.MarshalIndent(m.config, "", "  ")
	return m.cfg.WriteExtra("easytier", string(b))
}

// Configured 是否已配置完整（有 secret + relay_host + gateway_ipv4）。
func (m *EasyTierManager) Configured() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.config.NetworkSecret != "" && m.config.RelayHost != "" && m.config.GatewayIPv4 != ""
}

// PublicConfig 返回脱敏配置（network_secret 只暴露是否已设置）。
func (m *EasyTierManager) PublicConfig() map[string]any {
	m.mu.Lock()
	c := m.config
	m.mu.Unlock()
	return map[string]any{
		"network_name": c.NetworkName, "relay_host": c.RelayHost, "relay_port": c.RelayPort,
		"network_cidr": c.NetworkCIDR, "gateway_ipv4": c.GatewayIPv4, "mtu": c.MTU, "tun": c.Sudo,
		"network_secret_set": c.NetworkSecret != "",
		"binary":             m.corePath(),
	}
}

// Apply 处理平台下发的 easytier:config：保存并重启/启动（无人值守不弹授权框）。
func (m *EasyTierManager) Apply(raw json.RawMessage) error {
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	m.mu.Lock()
	if v, ok := p["network_name"].(string); ok && v != "" {
		m.config.NetworkName = v
	}
	if v, ok := p["network_secret"].(string); ok && v != "" {
		m.config.NetworkSecret = v
	}
	if v, ok := p["relay_host"].(string); ok && v != "" {
		m.config.RelayHost = v
	}
	if v, ok := p["relay_port"].(float64); ok {
		m.config.RelayPort = int(v)
	}
	if v, ok := p["network_cidr"].(string); ok && v != "" {
		m.config.NetworkCIDR = v
	}
	if v, ok := p["gateway_ipv4"].(string); ok && v != "" {
		m.config.GatewayIPv4 = v
	}
	if v, ok := p["mtu"].(float64); ok && v > 0 {
		m.config.MTU = int(v)
	}
	m.mu.Unlock()
	if err := m.save(); err != nil {
		return err
	}
	if !m.Configured() {
		return errors.New("easytier 配置不完整（缺 network_secret 或 relay_host）")
	}
	if m.Running() {
		_, err := m.Restart(false)
		return err
	}
	_, err := m.Start(false)
	return err
}

// ---- 生成 TOML / 启停 ----

func (m *EasyTierManager) proxyNetworks() []string {
	var out []string
	seen := map[string]bool{}
	for _, d := range m.cfg.Devices {
		ip := d.IP
		if ip == "" || !strings.Contains(ip, ".") {
			continue
		}
		parts := strings.Split(ip, ".")
		if len(parts) != 4 {
			continue
		}
		cidr := parts[0] + "." + parts[1] + "." + parts[2] + ".0/24"
		if !seen[cidr] {
			seen[cidr] = true
			out = append(out, cidr)
		}
	}
	return out
}

func (m *EasyTierManager) writeTOML() error {
	m.mu.Lock()
	c := m.config
	instance := m.cfg.Cloud.GatewayName
	if instance == "" {
		if h, err := os.Hostname(); err == nil {
			instance = h
		}
	}
	m.mu.Unlock()

	ipv4 := c.GatewayIPv4
	if ipv4 != "" && !strings.Contains(ipv4, "/") {
		prefix := "16"
		if s := strings.Split(c.NetworkCIDR, "/"); len(s) == 2 {
			prefix = s[1]
		}
		ipv4 = ipv4 + "/" + prefix
	}
	relayPort := c.RelayPort
	if relayPort == 0 {
		relayPort = 11010
	}

	var b strings.Builder
	b.WriteString("# EasyTier 节点配置（平台 easytier:config 动态下发）\n")
	b.WriteString(fmt.Sprintf("hostname = %q\n", instance))
	b.WriteString(fmt.Sprintf("instance_name = %q\n", instance))
	b.WriteString(fmt.Sprintf("ipv4 = %q\n", ipv4))
	b.WriteString("dhcp = false\n\n")
	b.WriteString(fmt.Sprintf("listeners = [\"udp://0.0.0.0:%d\", \"tcp://0.0.0.0:%d\", \"wg://0.0.0.0:%d\"]\n", relayPort, relayPort, relayPort+1))
	b.WriteString("rpc_portal = \"127.0.0.1:15888\"\n\n")
	b.WriteString("[network_identity]\n")
	b.WriteString(fmt.Sprintf("network_name = %q\n", c.NetworkName))
	b.WriteString(fmt.Sprintf("network_secret = %q\n", c.NetworkSecret))
	// 中继双协议 peer：UDP 常被网络路径丢包（表现为持续 connect timeout），
	// TCP 同端口兜底，easytier 会自动选用可用通道。
	b.WriteString("\n[[peer]]\n")
	b.WriteString(fmt.Sprintf("uri = \"udp://%s:%d\"\n", c.RelayHost, relayPort))
	b.WriteString("\n[[peer]]\n")
	b.WriteString(fmt.Sprintf("uri = \"tcp://%s:%d\"\n", c.RelayHost, relayPort))
	for _, cidr := range m.proxyNetworks() {
		b.WriteString("\n[[proxy_network]]\n")
		b.WriteString(fmt.Sprintf("cidr = %q\n", cidr))
	}
	mtu := c.MTU
	if mtu == 0 {
		mtu = 1380
	}
	b.WriteString("\n[flags]\n")
	b.WriteString("relay_all_peer_rpc = true\n")
	b.WriteString("latency_first = true\n")
	b.WriteString("default_protocol = \"udp\"\n")
	b.WriteString("enable_kcp_proxy = true\n")
	b.WriteString("enable_quic_proxy = true\n")
	b.WriteString("compression = \"zstd\"\n")
	b.WriteString(fmt.Sprintf("mtu = %d\n", mtu))
	b.WriteString("multi_thread = true\n")
	b.WriteString("bind_device = true\n")

	return os.WriteFile(m.tomlPath, []byte(b.String()), 0o600)
}

// Running 进程是否在运行。
func (m *EasyTierManager) Running() bool {
	m.mu.Lock()
	p := m.process
	m.mu.Unlock()
	if p == nil {
		return false
	}
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func (m *EasyTierManager) cmdArgs() []string {
	base := []string{m.corePath(), "--config-file", m.tomlPath, "--disable-env-parsing"}
	if os.Geteuid() == 0 {
		return base
	}
	return append([]string{"sudo", "-n"}, base...)
}

func (m *EasyTierManager) sudoOK() bool {
	return exec.Command("sudo", "-n", m.corePath(), "--version").Run() == nil
}

func (m *EasyTierManager) authorize() bool {
	if _, err := os.Stat(m.setupSudo); err != nil {
		return false
	}
	script := fmt.Sprintf(`do shell script "sh %s" with administrator privileges`, m.setupSudo)
	if exec.Command("osascript", "-e", script).Run() != nil {
		return false
	}
	return m.sudoOK()
}

// Start 启动 easytier；authorize=true 时未授权会弹系统授权框（仅首次）。
// startMu 串行化全部启动调用（手动/UI/看护自愈并发时只有一个真正拉起进程）。
func (m *EasyTierManager) Start(authorize bool) (bool, error) {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	if m.Running() {
		m.mu.Lock()
		m.desired = true
		m.mu.Unlock()
		return true, nil
	}
	if !m.Configured() {
		return false, errors.New("easytier 配置未就绪（缺 network_secret 或 gateway_ipv4）")
	}
	core := m.corePath()
	if _, err := os.Stat(core); err != nil {
		return false, fmt.Errorf("easytier-core 不存在：%s", core)
	}
	if os.Geteuid() != 0 && !m.sudoOK() {
		if authorize && m.authorize() {
			// 授权完成继续
		} else {
			return false, errors.New("开启虚拟网卡需管理员授权：请先在网关页点一次「启动」完成授权（仅首次）")
		}
	}
	if err := m.writeTOML(); err != nil {
		return false, err
	}
	// 清理遗留的孤儿进程（网关重启/异常退出时留下的 core 仍占用 11010/15888 端口，
	// 会导致新进程 "Address already in use" 起不来）。本网关无托管进程时，任何使用
	// 同一配置文件的 core 都是孤儿，直接清掉。
	m.killStale()
	args := m.cmdArgs()
	cmd := exec.Command(args[0], args[1:]...)
	if f, err := os.OpenFile(m.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		cmd.Stdout, cmd.Stderr = f, f
	}
	if err := cmd.Start(); err != nil {
		return false, err
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	m.mu.Lock()
	m.process = &easyTierProc{cmd: cmd, done: done}
	m.mu.Unlock()
	// 进程意外退出时留痕，方便排查"启动一会就停止"。
	go func() {
		<-done
		m.mu.Lock()
		current := m.process
		m.mu.Unlock()
		if current != nil && current.cmd == cmd {
			slog.Warn("easytier-core 进程退出", "log", m.logPath)
		}
	}()

	for i := 0; i < 25; i++ {
		select {
		case <-done:
			return false, errors.New("easytier-core 进程退出（见 /tmp/easytier-gateway.log）")
		default:
		}
		if m.cliOK() {
			m.mu.Lock()
			m.desired = true
			m.mu.Unlock()
			return true, nil
		}
		time.Sleep(time.Second)
	}
	ok := m.cliOK()
	m.mu.Lock()
	m.desired = ok
	m.mu.Unlock()
	return ok, nil
}

// Supervise 看护自愈：用户意图为运行（desired）但进程不在时自动拉回；
// 2 分钟限速防崩溃风暴，主动停止（desired=false）绝不自动重启。
func (m *EasyTierManager) Supervise() {
	m.mu.Lock()
	desired := m.desired
	m.mu.Unlock()
	if !desired || m.Running() || !m.Configured() {
		return
	}
	m.mu.Lock()
	if time.Since(m.lastRestart) < 2*time.Minute {
		m.mu.Unlock()
		return
	}
	m.lastRestart = time.Now()
	m.mu.Unlock()
	slog.Warn("easytier-core 意外退出，看护自动拉起")
	go func() {
		if _, err := m.Start(false); err != nil {
			slog.Warn("easytier auto-restart failed", "error", err)
		}
	}()
}

// killStale 杀掉所有遗留 core（含孤儿与 sudo 包装进程），不限定配置文件路径。
func (m *EasyTierManager) killStale() {
	_ = exec.Command("sudo", "-n", "pkill", "-f", "easytier-core").Run()
	time.Sleep(500 * time.Millisecond) // 等端口释放
}

// Stop 停止 easytier（含遗留 root 进程）；记录用户意图为「已停止」，看护不再自动拉起。
func (m *EasyTierManager) Stop() bool {
	m.mu.Lock()
	m.desired = false
	m.mu.Unlock()
	m.mu.Lock()
	p := m.process
	m.process = nil
	m.mu.Unlock()
	stopped := false
	if p != nil {
		_ = p.cmd.Process.Signal(syscall.SIGINT)
		select {
		case <-p.done:
			stopped = true
		case <-time.After(8 * time.Second):
			_ = p.cmd.Process.Kill()
			stopped = true
		}
	}
	m.killStale()
	return stopped
}

// Restart 重启。
func (m *EasyTierManager) Restart(authorize bool) (bool, error) {
	m.Stop()
	time.Sleep(time.Second)
	return m.Start(authorize)
}

// Recover 网关重启后自愈：杀掉遗留进程并按最新配置重启。
func (m *EasyTierManager) Recover() {
	if !m.Configured() {
		return
	}
	m.killStale()
	if _, err := m.Start(false); err != nil {
		slog.Warn("easytier recover start failed", "error", err)
	}
}

// ---- RPC 查询 ----

func (m *EasyTierManager) cli(args ...string) ([]byte, error) {
	full := append([]string{"-p", m.rpcPortal}, args...)
	return exec.Command(m.cliPath(), full...).Output()
}

func (m *EasyTierManager) cliOK() bool {
	_, err := m.cli("node", "info")
	return err == nil
}

func (m *EasyTierManager) nodeInfo() map[string]any {
	out, err := m.cli("-o", "json", "node", "info")
	if err != nil {
		return nil
	}
	var node map[string]any
	if json.Unmarshal(out, &node) != nil {
		return nil
	}
	delete(node, "config") // 脱敏：config 含 network_secret 明文
	return node
}

func (m *EasyTierManager) peers() []map[string]any {
	out, err := m.cli("-o", "json", "peer", "list")
	if err != nil {
		return []map[string]any{}
	}
	var peers []map[string]any
	if json.Unmarshal(out, &peers) != nil {
		return []map[string]any{}
	}
	if peers == nil {
		peers = []map[string]any{}
	}
	return peers
}

// Status 返回 easytier 状态快照。
func (m *EasyTierManager) Status() map[string]any {
	running := m.Running()
	var node map[string]any
	var peers []map[string]any
	var errMsg any
	if running {
		node = m.nodeInfo()
		peers = m.peers()
		if node == nil {
			errMsg = "easytier-core 运行中但 RPC 不可达（见 /tmp/easytier-gateway.log）"
		}
	}
	pid := 0
	m.mu.Lock()
	if running && m.process != nil && m.process.cmd.Process != nil {
		pid = m.process.cmd.Process.Pid
	}
	m.mu.Unlock()
	return map[string]any{
		"configured": m.Configured(), "running": running, "pid": pid,
		"node": node, "peers": peers, "error": errMsg, "log": m.logPath,
	}
}
