package gateway

import (
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
	buildMu     sync.Mutex // 仅串行化 build-for-testing；构建耗时数分钟，绝不能拿 mu，否则期间 Running/Stop/激活全部被拖死
	xctestrun   string
	projectRoot string
	derivedData string
	team        string // DEVELOPMENT_TEAM 透传（空=工程内默认值）
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
		return "", fmt.Errorf("build-for-testing: %w", err)
	}
	hits, _ := filepath.Glob(filepath.Join(m.derivedData, "Build", "Products", "WebDriverAgentRunner_iphoneos*.xctestrun"))
	if len(hits) == 0 {
		return "", fmt.Errorf("xctestrun not found after build")
	}
	m.xctestrun = hits[0]
	slog.Info("WDA build finished", "xctestrun", m.xctestrun)
	return m.xctestrun, nil
}

// Activate 激活单台 WDA（xcodebuild test-without-building）。
func (m *WDAManager) Activate(udid string, port int, reportedUDID string) error {
	if port == 0 {
		port = 8100
	}
	if reportedUDID == "" {
		reportedUDID = udid
	}
	if m.Running(udid) {
		return nil // 已在运行
	}
	m.mu.Lock()
	cool := m.crashUntil[udid]
	m.mu.Unlock()
	if time.Now().Before(cool) {
		return fmt.Errorf("WDA 上次启动后很快退出，%.0f 秒内暂停自动重激活（疑似签名/信任问题，需在设备上信任开发者后重试）", time.Until(cool).Seconds())
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
	// iOS 16+ 真机 UDID 是 8-16 hex 带连字符（如 00008120-000865D90A10C01E），
	// Xcode 16.4 下 `-destination id=<udid>` 匹配不到（报 Unable to find a device），
	// 必须带 platform 前缀：`platform=iOS,id=<udid>`（实测可正常拉起 WDA）。
	cmd := exec.Command("xcodebuild", "-xctestrun", tmp, "-destination", "platform=iOS,id="+udid, "test-without-building")
	cmd.Env = env
	logPath := filepath.Join("/tmp", "wda-"+udid[:8]+".log")
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		cmd.Stdout, cmd.Stderr = f, f
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	p := &wdaProc{cmd: cmd, done: make(chan struct{})}
	m.mu.Lock()
	m.processes[udid] = p
	m.startedAt[udid] = time.Now()
	m.mu.Unlock()
	// 唯一的 Wait 调用者：进程退出即关 done 并清理表项。
	// 没有它，ProcessState 永远是 nil，Running() 会对已死进程永远返回 true，
	// 看护循环也因此永远不会重激活（设备假在线、实际已挂）。
	go func() {
		err := cmd.Wait()
		close(p.done)
		m.mu.Lock()
		started := m.startedAt[udid]
		if cur, ok := m.processes[udid]; ok && cur == p {
			delete(m.processes, udid)
			delete(m.startedAt, udid)
		}
		// 启动 2 分钟内即退出：大概率是签名/信任类故障（exit 65），进入 5 分钟冷却，
		// 避免看护循环每 30s 重新拉起 xcodebuild 的重试风暴。
		if !started.IsZero() && time.Since(started) < 2*time.Minute {
			m.crashUntil[udid] = time.Now().Add(5 * time.Minute)
		}
		m.mu.Unlock()
		if err != nil {
			slog.Warn("WDA process exited", "udid", udid[:8], "error", err)
		}
	}()
	return nil
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
