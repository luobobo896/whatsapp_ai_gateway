package gateway

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// wdaProc 一台设备的 xcodebuild 进程；done 在进程退出后被关闭
// （由 Activate 启动的唯一一个 Wait 协程负责，避免并发 Wait 竞态）。
type wdaProc struct {
	cmd  *exec.Cmd
	done chan struct{}
}

// WDAManager 按 UDID 激活/停止/看护 WDA（xcodebuild build-for-testing + test-without-building）。
type WDAManager struct {
	mu          sync.Mutex
	processes   map[string]*wdaProc
	startedAt   map[string]time.Time
	crashUntil  map[string]time.Time // 启动后很快退出的冷却截止（避免签名/信任类故障重试风暴）
	buildMu     sync.Mutex           // 仅串行化 build-for-testing；构建耗时数分钟，绝不能拿 mu，否则期间 Running/Stop/激活全部被拖死
	xctestrun   string
	projectRoot string
	derivedData string
	team        string // DEVELOPMENT_TEAM 透传（空=工程内默认值）
	activator   string // auto|xcodebuild|goios|tidevice
	bundleID    string // 已安装 WDA Runner 的 bundle id
	ipaPath     string // 已签名 IPA；协议激活时未装 Runner 会先 install
}

// NewWDAManager 构造管理器；projectRoot 为 WhatsAppDeviceAgent 工程路径。
// team 非空时透传给 xcodebuild 的 DEVELOPMENT_TEAM（换签名账号用），空则用工程内写死值。
func NewWDAManager(projectRoot, derivedData, team string) *WDAManager {
	if derivedData == "" {
		derivedData = "/tmp/WebDriverAgentFarmDerived"
	}
	return &WDAManager{
		processes:   map[string]*wdaProc{},
		startedAt:   map[string]time.Time{},
		crashUntil:  map[string]time.Time{},
		projectRoot: projectRoot,
		derivedData: derivedData,
		team:        team,
		activator:   "auto",
		bundleID:    defaultWDABundleID,
	}
}

// ConfigureSigning 应用签名/激活后端配置（启动时从 Config.Signing 注入）。
func (m *WDAManager) ConfigureSigning(s SigningConfig) {
	if s.Team != "" {
		m.team = s.Team
	}
	if s.Activator != "" {
		m.activator = s.Activator
	}
	if s.WDABundleID != "" {
		m.bundleID = s.WDABundleID
	}
	if strings.TrimSpace(s.IPAPath) != "" {
		m.ipaPath = strings.TrimSpace(s.IPAPath)
	}
}

// Running 返回指定 UDID 的 WDA 进程是否在运行。
func (m *WDAManager) Running(udid string) bool {
	m.mu.Lock()
	p := m.processes[udid]
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

// StartedSecondsAgo 进程已启动秒数（无进程返回 0）。
func (m *WDAManager) StartedSecondsAgo(udid string) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.startedAt[udid]
	if !ok || m.processes[udid] == nil {
		return 0
	}
	return time.Since(t).Seconds()
}

// findExistingXctestrun 在 derived 里找已编过的 xctestrun。
// 优先不含 .runtime. 的模板（Activate 会按 UDID 再拷一份）。
func findExistingXctestrun(derived string) string {
	hits, _ := filepath.Glob(filepath.Join(derived, "Build", "Products", "WebDriverAgentRunner_iphoneos*.xctestrun"))
	var fallback string
	for _, h := range hits {
		if strings.Contains(filepath.Base(h), ".runtime.") {
			if fallback == "" {
				fallback = h
			}
			continue
		}
		return h
	}
	return fallback
}

// ensureBuilt 构建一次 WDA，产物复用。
func (m *WDAManager) ensureBuilt() (string, error) {
	// buildMu 只串行化构建本身；持有期间 mu 上的 Running/Stop/Activate 照常工作。
	m.buildMu.Lock()
	defer m.buildMu.Unlock()
	if m.xctestrun != "" {
		if _, err := os.Stat(m.xctestrun); err == nil {
			return m.xctestrun, nil
		}
	}
	if existing := findExistingXctestrun(m.derivedData); existing != "" {
		m.xctestrun = existing
		slog.Info("reuse existing WDA product", "xctestrun", existing)
		return existing, nil
	}
	slog.Info("WDA product not built, running build-for-testing (may take minutes)")
	args := []string{
		"-project", filepath.Join(m.projectRoot, "WebDriverAgent.xcodeproj"),
		"-scheme", "WebDriverAgentRunner", "-configuration", "Debug",
		"-destination", "generic/platform=iOS",
		"-derivedDataPath", m.derivedData,
		"-allowProvisioningUpdates",
		"ENABLE_DEFAULT_HEADER_SEARCH_PATHS=NO",
		"GCC_TREAT_WARNINGS_AS_ERRORS=NO",
		"OTHER_CFLAGS=$(inherited) -Wno-error=poison-system-directories",
		"RUN_CLANG_STATIC_ANALYZER=NO",
	}
	if m.team != "" {
		args = append(args, "DEVELOPMENT_TEAM="+m.team)
	}
	args = append(args, "build-for-testing")
	cmd := exec.Command("xcodebuild", args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build-for-testing: %w（Xcode 找不到 iOS 目标；若本机缺 iOS Platform，应复用已有 xctestrun，不要反复点激活）", err)
	}
	existing := findExistingXctestrun(m.derivedData)
	if existing == "" {
		return "", fmt.Errorf("xctestrun not found after build")
	}
	m.xctestrun = existing
	slog.Info("WDA build finished", "xctestrun", m.xctestrun)
	return m.xctestrun, nil
}

// destinationForUDID 生成 xcodebuild -destination 参数。
//   - 40 位老格式 UDID（iOS ≤15，如 iPhone 7 Plus 5060c403af...）：必须用无前缀
//     `id=<udid>`。带 `platform=iOS,id=` 前缀在 Xcode 16 上匹配不到 iOS 15 老设备
//     （实测报 Unable to find a device，无前缀可正常安装并启动 WDA）。
//   - 8-16 带连字符格式（iOS 16+，如 00008120-...）：必须带 `platform=iOS,id=`
//     前缀且 UDID 大写（normalizeUDID，小写 exit 70）。
func destinationForUDID(udid string) string {
	if strings.Contains(udid, "-") {
		return "platform=iOS,id=" + normalizeUDID(udid)
	}
	return "id=" + udid
}

// Activate 激活单台 WDA。via 为 usb 或 network，决定 XCTest 走哪条 usbmux 通道。
// 日常路径是 IPA：缺 Runner 就 install，再 tidevice/go-ios 拉起。
// 只有 auto 找不到协议工具时，Mac 才回退 xcodebuild。
func (m *WDAManager) Activate(udid string, port int, reportedUDID, via string) error {
	if port == 0 {
		port = 8100
	}
	if reportedUDID == "" {
		reportedUDID = udid
	}
	via = parseActivateVia(via)
	if m.Running(udid) {
		return nil // 已在运行；换通道由调用方先 Stop
	}
	m.mu.Lock()
	cool := m.crashUntil[udid]
	m.mu.Unlock()
	if time.Now().Before(cool) {
		return fmt.Errorf("WDA 激活后未保持运行（原因见激活日志；常见为设备锁屏/未解锁、未授权自动化，或无线调试配对缺失）。%.0f 秒内暂停自动重激活；处理后可点“激活”立即重试", time.Until(cool).Seconds())
	}

	kind := resolveActivator(m.activator)
	if via == activateViaNetwork && kind == activatorXcodebuild {
		return fmt.Errorf("Network 激活不能走 xcodebuild（xcodebuild 走 USB，不会回退）")
	}
	if kind == activatorGoIOS || kind == activatorTidevice {
		return m.activateProtocol(udid, port, reportedUDID, kind, via)
	}
	return m.activateXcodebuild(udid, port, reportedUDID)
}

func (m *WDAManager) activateXcodebuild(udid string, port int, reportedUDID string) error {
	// 激活前确保 iOS ≤16 老设备（iPhone 7/8/X 等）的 DeveloperDiskImage 就绪：
	// Xcode 的 DeviceSupport 目录通常缺 DDI，导致 xcodebuild 挂载/安装 WDA 失败。
	// 幂等；失败仅告警不阻塞（iOS 17+ 走 CoreDevice 原生支持，会静默跳过）。
	if err := EnsureDeviceSupportDDI(udid); err != nil {
		slog.Warn("EnsureDeviceSupportDDI failed", "udid", shortOf(udid), "error", err)
	}

	xctestrun, err := m.ensureBuilt()
	if err != nil {
		return err
	}
	// 临时 xctestrun 必须按设备隔离：多台同轮激活共用一个文件会互相覆盖
	// 注入的 WDA_DEVICE_UDID/USE_PORT，导致设备身份窜号与进程冲突退出。
	tmp := fmt.Sprintf("%s.%s.runtime.xctestrun", xctestrun, udid[:8])
	if err := copyFile(xctestrun, tmp); err != nil {
		return err
	}
	// 与 scripts/start-wda.sh 一致：把 USE_PORT / WDA_DEVICE_UDID 注入 xctestrun 的 EnvironmentVariables。
	env := os.Environ()
	env = append(env, "EXPANDED_CODE_SIGN_IDENTITY="+signingIdentity())
	_ = plistSet(tmp, "WebDriverAgentRunner:EnvironmentVariables:USE_PORT", fmt.Sprint(port))
	_ = plistSet(tmp, "WebDriverAgentRunner:EnvironmentVariables:WDA_DEVICE_UDID", reportedUDID)
	// destination 按 UDID 格式区分（destinationForUDID）：
	// 1) iOS 16+ 8-16 hex 带连字符（如 00008120-000865D90A10C01E）：必须带
	//    `platform=iOS,id=...` 前缀且大写（小写 exit 70）；
	// 2) iOS ≤15 老格式 40 位 hex（如 5060c403...）：带前缀匹配不到，必须用无前缀
	//    `id=...`（Xcode 16 实测，无前缀可正常安装启动 WDA）。
	cmd := exec.Command("xcodebuild", "-xctestrun", tmp, "-destination", destinationForUDID(udid), "test-without-building")
	cmd.Env = env
	logPath := filepath.Join("/tmp", "wda-"+udid[:8]+".log")
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		cmd.Stdout, cmd.Stderr = f, f
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	m.track(udid, cmd)
	return nil
}

// track 登记激活进程：唯一 Wait 调用者。进程退出即关 done 并清理表项。
// 没有它，ProcessState 永远是 nil，Running() 会对已死进程永远返回 true，
// 看护循环也因此永远不会重激活（设备假在线、实际已挂）。
func (m *WDAManager) track(udid string, cmd *exec.Cmd) {
	p := &wdaProc{cmd: cmd, done: make(chan struct{})}
	m.mu.Lock()
	m.processes[udid] = p
	m.startedAt[udid] = time.Now()
	m.mu.Unlock()
	go func() {
		err := cmd.Wait()
		close(p.done)
		m.mu.Lock()
		started := m.startedAt[udid]
		if cur, ok := m.processes[udid]; ok && cur == p {
			delete(m.processes, udid)
			delete(m.startedAt, udid)
		}
		// 启动 2 分钟内即退出：大概率是签名/信任类故障（exit 65），进入 5 分钟冷却。
		// 拔 USB 导致的 host 退出（exit 75）不是崩溃：机上 XCTest 仍可走 Wi-Fi。
		if !started.IsZero() && time.Since(started) < 2*time.Minute && !hostProcDetachExpected(err) {
			m.crashUntil[udid] = time.Now().Add(5 * time.Minute)
		}
		m.mu.Unlock()
		if err != nil {
			if hostProcDetachExpected(err) {
				slog.Info("WDA host process exited after USB detach; device WDA may stay on Wi-Fi",
					"udid", shortOf(udid), "error", err)
			} else {
				slog.Warn("WDA process exited", "udid", shortOf(udid), "error", err,
					"detail", activationFailureDetail(udid))
			}
		}
	}()
}

// hostProcDetachExpected：激活进程因设备断开而退出（拔 USB），不是签名崩溃。
func hostProcDetachExpected(err error) bool {
	if err == nil {
		return false
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 75 {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "exit status 75") ||
		strings.Contains(s, "was disconnected") ||
		strings.Contains(s, "not connected")
}

// activationFailureDetail 从本次激活日志（<TMPDIR>/wda-<udid>.log）里取最后一条
// ERROR / “Failed running WDA” 的可读描述，把 go-ios 的真实原因带进网关日志，
// 避免只看到 exit status 1 而误判成“签名/信任问题”。
func activationFailureDetail(udid string) string {
	logPath := filepath.Join(os.TempDir(), "wda-"+shortOf(udid)+".log")
	b, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(b), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		if strings.Contains(l, `"level":"ERROR"`) || strings.Contains(l, "Failed running WDA") {
			if m := extractJSONField(l, "error"); m != "" {
				return m
			}
			if m := extractJSONField(l, "msg"); m != "" {
				return m
			}
			return strings.TrimSpace(l)
		}
	}
	return ""
}

// extractJSONField 从一行 JSON 里取 `"field":"..."` 的值（忽略转义引号）。
func extractJSONField(line, field string) string {
	key := `"` + field + `":"`
	i := strings.Index(line, key)
	if i < 0 {
		return ""
	}
	rest := line[i+len(key):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

// ResetCrashCooldown 清除某设备的崩溃冷却（管理页手动「激活」时调用，允许人工立即重试）。
func (m *WDAManager) ResetCrashCooldown(udid string) {
	m.mu.Lock()
	delete(m.crashUntil, udid)
	m.mu.Unlock()
}

// Stop 停止单台 WDA。
func (m *WDAManager) Stop(udid string) bool {
	m.mu.Lock()
	p := m.processes[udid]
	delete(m.processes, udid)
	delete(m.startedAt, udid)
	m.mu.Unlock()
	if p == nil || p.cmd.Process == nil {
		return false
	}
	_ = p.cmd.Process.Signal(os.Interrupt)
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		_ = p.cmd.Process.Kill()
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
		}
	}
	return true
}

func signingIdentity() string {
	out, err := exec.Command("security", "find-identity", "-v", "-p", "codesigning").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "CSSMERR_TP_CERT_REVOKED") || !strings.Contains(line, "Apple Development") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) > 1 {
			return parts[1]
		}
	}
	return ""
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

// plistSet 用 PlistBuddy 设置/替换 xctestrun 里 <key>:<value>（失败返回 err，但不中断激活）。
func plistSet(path, key, value string) error {
	_ = exec.Command("/usr/libexec/PlistBuddy", "-c", "Delete :"+key, path).Run()
	return exec.Command("/usr/libexec/PlistBuddy", "-c", "Add :"+key+" string "+value, path).Run()
}
