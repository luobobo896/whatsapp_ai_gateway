package gateway

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const (
	activatorAuto       = "auto"
	activatorXcodebuild = "xcodebuild"
	activatorGoIOS      = "goios"
	activatorTidevice   = "tidevice"

	// defaultWDABundleID 与 WhatsAppDeviceAgent 工程 PRODUCT_BUNDLE_IDENTIFIER 对齐，
	// Xcode 安装 UI test runner 时会加 .xctrunner 后缀。
	defaultWDABundleID  = "com.wda.WebRunner.xctrunner"
	defaultXCTestConfig = "WebDriverAgentRunner.xctest"
)

// resolveActivator 把配置值收成实际后端。auto：Windows 走 goios，其余走 xcodebuild。
func resolveActivator(configured string) string {
	switch strings.ToLower(strings.TrimSpace(configured)) {
	case activatorGoIOS:
		return activatorGoIOS
	case activatorTidevice:
		return activatorTidevice
	case activatorXcodebuild:
		return activatorXcodebuild
	default:
		if runtime.GOOS == "windows" {
			return activatorGoIOS
		}
		return activatorXcodebuild
	}
}

func (m *WDAManager) wdaBundleID() string {
	if m != nil && strings.TrimSpace(m.bundleID) != "" {
		return strings.TrimSpace(m.bundleID)
	}
	return defaultWDABundleID
}

func (m *WDAManager) activateProtocol(udid string, port int, reportedUDID, kind string) error {
	bin, args, err := m.protocolCmd(udid, port, reportedUDID, kind)
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), bundleLibFallback()...)
	logPath := filepath.Join(os.TempDir(), "wda-"+udid[:8]+".log")
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		cmd.Stdout, cmd.Stderr = f, f
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s start: %w", kind, err)
	}
	m.track(udid, cmd)
	return nil
}

func (m *WDAManager) protocolCmd(udid string, port int, reportedUDID, kind string) (string, []string, error) {
	bundle := m.wdaBundleID()
	switch kind {
	case activatorGoIOS:
		bin := lookTool("ios", "ios.exe")
		if bin == "" {
			return "", nil, fmt.Errorf("未找到 go-ios（ios）：Windows 激活需要把它放到 PATH 或 WDA_GATEWAY_RESOURCES/bin")
		}
		return bin, goiosArgs(udid, bundle, port, reportedUDID), nil
	case activatorTidevice:
		bin := lookTool("tidevice", "tidevice.exe")
		if bin == "" {
			return "", nil, fmt.Errorf("未找到 tidevice：请安装后放到 PATH 或 WDA_GATEWAY_RESOURCES/bin")
		}
		return bin, tideviceArgs(udid, bundle, port, reportedUDID), nil
	default:
		return "", nil, fmt.Errorf("未知激活后端 %q", kind)
	}
}

// goiosArgs 构造 `ios runwda` 参数（不含可执行文件本身）。
// 设备必须已安装并信任对应签名的 WDA Runner；本命令只负责经 testmanagerd 拉起 XCTest。
func goiosArgs(udid, bundleID string, port int, reportedUDID string) []string {
	if reportedUDID == "" {
		reportedUDID = udid
	}
	return []string{
		"--udid=" + udid,
		"runwda",
		"--bundleid=" + bundleID,
		"--testrunnerbundleid=" + bundleID,
		"--xctestconfig=" + defaultXCTestConfig,
		"--env=USE_PORT=" + strconv.Itoa(port),
		"--env=WDA_DEVICE_UDID=" + reportedUDID,
	}
}

// tideviceArgs 构造 `tidevice xctest` 参数。用 xctest 而不是 wdaproxy，
// 避免和网关已有的 iproxy USB 隧道抢同一套端口转发。
func tideviceArgs(udid, bundleID string, port int, reportedUDID string) []string {
	if reportedUDID == "" {
		reportedUDID = udid
	}
	// tidevice 的 -e/--env 格式是 KEY:VALUE（冒号），不是 go-ios 的 KEY=VALUE。
	return []string{
		"-u", udid,
		"xctest",
		"-B", bundleID,
		"-e", "USE_PORT:" + strconv.Itoa(port),
		"-e", "WDA_DEVICE_UDID:" + reportedUDID,
	}
}

// lookTool 与 libiDeviceBin 同策略：PATH → bundle Resources/bin → 常见绝对路径。
func lookTool(unixName, windowsName string) string {
	names := []string{unixName}
	if runtime.GOOS == "windows" && windowsName != "" {
		names = []string{windowsName, unixName}
	}
	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	var candidates []string
	if res := os.Getenv("WDA_GATEWAY_RESOURCES"); res != "" {
		for _, name := range names {
			candidates = append(candidates, filepath.Join(res, "bin", name))
		}
	}
	if runtime.GOOS != "windows" {
		candidates = append(candidates, "/opt/homebrew/bin/"+unixName, "/usr/local/bin/"+unixName)
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// pingArgs 探测 IPv4 可达性。Windows 的 ping 开关与 Unix 不同。
func pingArgs(ip string) []string {
	if runtime.GOOS == "windows" {
		return []string{"-n", "1", "-w", "2000", ip}
	}
	return []string{"-c", "1", "-t", "2", ip}
}
