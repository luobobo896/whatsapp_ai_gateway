package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
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

// resolveActivator 把配置值收成实际后端。
// auto：有 go-ios 用 goios，否则有 tidevice 用 tidevice；都没有时 Windows 仍选 goios（启动时报缺二进制），Mac 才回退 xcodebuild。
func resolveActivator(configured string) string {
	switch strings.ToLower(strings.TrimSpace(configured)) {
	case activatorGoIOS:
		return activatorGoIOS
	case activatorTidevice:
		return activatorTidevice
	case activatorXcodebuild:
		return activatorXcodebuild
	default:
		return autoActivator(lookTool("ios", "ios.exe") != "", lookTool("tidevice", "tidevice.exe") != "")
	}
}

func autoActivator(hasGoIOS, hasTidevice bool) string {
	return autoActivatorFor(hasGoIOS, hasTidevice, runtime.GOOS)
}

func autoActivatorFor(hasGoIOS, hasTidevice bool, goos string) string {
	if hasGoIOS {
		return activatorGoIOS
	}
	if hasTidevice {
		return activatorTidevice
	}
	if goos == "windows" {
		return activatorGoIOS
	}
	return activatorXcodebuild
}

// enableWifiLockdown 打开 lockdown EnableWifiConnections（Xcode「Connect via network」）。
// 不打开时拔 USB 会拆掉 testmanagerd，机上 WDA 一起死。没有辅助程序就跳过。
func enableWifiLockdown(udid string) {
	if udid == "" {
		return
	}
	bin := lookTool("wifi-lockdown", "wifi-lockdown.exe")
	if bin == "" {
		return
	}
	if _, err := runTool(bin, []string{udid}, 15*time.Second); err != nil {
		slog.Warn("enable wifi lockdown failed", "udid", shortOf(udid), "error", err)
		return
	}
	// 给 usbmuxd 一点时间挂上 Network 条目；真正的等待在 wifi-runwda -wait-network。
	time.Sleep(2 * time.Second)
	slog.Info("wifi lockdown enabled", "udid", shortOf(udid))
}

func (m *WDAManager) wdaBundleID() string {
	if m != nil && strings.TrimSpace(m.bundleID) != "" {
		return strings.TrimSpace(m.bundleID)
	}
	return defaultWDABundleID
}

func (m *WDAManager) activateProtocol(udid string, port int, reportedUDID, kind, wifiIP string) error {
	ver := resolveIOSVersion(udid, "")
	plan := planForIOS(ver)
	slog.Info("ios activate plan", "udid", shortOf(udid), "ios", ver, "major", plan.Major,
		"ddi", plan.NeedDDI, "tunnel", plan.NeedTunnel, "devmode", plan.NeedDevMode, "userspace_ok", plan.UserspaceOK)

	if plan.NeedTunnel && kind != activatorGoIOS {
		if lookTool("ios", "ios.exe") != "" {
			slog.Info("iOS 17+ switching activator to go-ios", "was", kind, "udid", shortOf(udid))
			kind = activatorGoIOS
		} else {
			return fmt.Errorf("iOS %s 需要 go-ios 隧道（ios tunnel start），未找到 ios/ios.exe", verOrUnknown(ver))
		}
	}

	enableWifiLockdown(udid)
	if err := m.prepareDeviceForIOS(udid, kind, ver, plan); err != nil {
		return err
	}
	if err := m.ensureRunnerInstalled(udid, kind, ver); err != nil {
		return err
	}
	bin, args, err := m.protocolCmd(udid, port, reportedUDID, kind, wifiIP, ver)
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

func (m *WDAManager) protocolCmd(udid string, port int, reportedUDID, kind, wifiIP, iosVersion string) (string, []string, error) {
	bundle := m.wdaBundleID()
	switch kind {
	case activatorGoIOS:
		// iOS 17+ 必须走 go-ios 自己的 RSD 隧道；wifi-runwda 劫持 usbmux 会和它打架。
		if !needsRemoteXPCTunnel(iosVersion) {
			if bin, args, ok := wifiRunwdaInvocation(udid, wifiIP, port, reportedUDID, bundle); ok {
				slog.Info("activate via wifi-runwda", "udid", shortOf(udid), "ip", wifiIP, "ios", iosVersion)
				return bin, args, nil
			}
		}
		bin := lookTool("ios", "ios.exe")
		if bin == "" {
			return "", nil, fmt.Errorf("未找到 go-ios（ios）：请把它放到 PATH 或 WDA_GATEWAY_RESOURCES/bin")
		}
		args := goiosArgs(udid, bundle, port, reportedUDID)
		if needsRemoteXPCTunnel(iosVersion) {
			args = withGoIOSTunnelPort(args)
		}
		return bin, args, nil
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

// wifiRunwdaInvocation 在本机同时有 wifi-runwda 和 ios 时，用包装器拉起 go-ios。
// 包装器会优先走 usbmux Network，让 XCTest 调试会话不绑死 USB。
func wifiRunwdaInvocation(udid, wifiIP string, port int, reportedUDID, bundleID string) (string, []string, bool) {
	wrap := lookTool("wifi-runwda", "wifi-runwda.exe")
	if wrap == "" {
		return "", nil, false
	}
	iosBin := lookTool("ios", "ios.exe")
	if iosBin == "" {
		return "", nil, false
	}
	if bundleID == "" {
		bundleID = defaultWDABundleID
	}
	args := []string{
		"-udid", udid,
		"-port", strconv.Itoa(port),
		"-bundle", bundleID,
		"-ios", iosBin,
	}
	if wifiIP != "" {
		args = append(args, "-ip", wifiIP)
	}
	return wrap, args, true
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

func goiosInstallArgs(udid, ipa string) []string {
	return []string{"--udid=" + udid, "install", "--path=" + ipa}
}

func goiosAppsArgs(udid string) []string {
	return []string{"--udid=" + udid, "apps"}
}

func tideviceInstallArgs(udid, ipa string) []string {
	return []string{"-u", udid, "install", ipa}
}

func tideviceAppsArgs(udid string) []string {
	return []string{"-u", udid, "applist"}
}

func appListContains(out, bundleID string) bool {
	bundleID = strings.TrimSpace(bundleID)
	return bundleID != "" && strings.Contains(out, bundleID)
}

func (m *WDAManager) resolvedIPA() string {
	p := ""
	if m != nil {
		p = strings.TrimSpace(m.ipaPath)
	}
	if p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if res := os.Getenv("WDA_GATEWAY_RESOURCES"); res != "" {
		cand := filepath.Join(res, "wda.ipa")
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return p
}

func (m *WDAManager) installCmd(udid, kind, ipa, iosVersion string) (string, []string, error) {
	switch kind {
	case activatorGoIOS:
		bin := lookTool("ios", "ios.exe")
		if bin == "" {
			return "", nil, fmt.Errorf("未找到 go-ios（ios）：安装 IPA 需要把它放到 PATH 或 WDA_GATEWAY_RESOURCES/bin")
		}
		args := goiosInstallArgs(udid, ipa)
		if needsRemoteXPCTunnel(iosVersion) {
			args = withGoIOSTunnelPort(args)
		}
		return bin, args, nil
	case activatorTidevice:
		bin := lookTool("tidevice", "tidevice.exe")
		if bin == "" {
			return "", nil, fmt.Errorf("未找到 tidevice：安装 IPA 需要把它放到 PATH 或 WDA_GATEWAY_RESOURCES/bin")
		}
		return bin, tideviceInstallArgs(udid, ipa), nil
	default:
		return "", nil, fmt.Errorf("未知激活后端 %q", kind)
	}
}

func (m *WDAManager) listAppsCmd(udid, kind, iosVersion string) (string, []string, error) {
	switch kind {
	case activatorGoIOS:
		bin := lookTool("ios", "ios.exe")
		if bin == "" {
			return "", nil, fmt.Errorf("未找到 go-ios（ios）")
		}
		args := goiosAppsArgs(udid)
		if needsRemoteXPCTunnel(iosVersion) {
			args = withGoIOSTunnelPort(args)
		}
		return bin, args, nil
	case activatorTidevice:
		bin := lookTool("tidevice", "tidevice.exe")
		if bin == "" {
			return "", nil, fmt.Errorf("未找到 tidevice")
		}
		return bin, tideviceAppsArgs(udid), nil
	default:
		return "", nil, fmt.Errorf("未知激活后端 %q", kind)
	}
}

func (m *WDAManager) ensureRunnerInstalled(udid, kind, iosVersion string) error {
	installed, listErr := m.runnerInstalled(udid, kind, iosVersion)
	if listErr != nil {
		slog.Warn("list installed apps failed", "udid", shortOf(udid), "error", listErr)
	}
	if installed {
		return nil
	}
	ipa := m.resolvedIPA()
	if ipa == "" {
		if listErr != nil {
			return nil
		}
		return fmt.Errorf("手机未安装 %s，且未配置 IPA。请先在 Mac 上运行 scripts/package-wda-ipa.sh，把 wda.ipa 放到网关状态目录或用 -ipa 指定", m.wdaBundleID())
	}
	if _, err := os.Stat(ipa); err != nil {
		if listErr != nil {
			return nil
		}
		return fmt.Errorf("手机未安装 %s，且找不到 IPA %s。请先在 Mac 上运行 scripts/package-wda-ipa.sh，把打好的包放到该路径", m.wdaBundleID(), ipa)
	}
	return m.installIPA(udid, kind, ipa, iosVersion)
}

func (m *WDAManager) runnerInstalled(udid, kind, iosVersion string) (bool, error) {
	bin, args, err := m.listAppsCmd(udid, kind, iosVersion)
	if err != nil {
		return false, err
	}
	out, err := runTool(bin, args, 20*time.Second)
	if err != nil {
		return false, err
	}
	return appListContains(out, m.wdaBundleID()), nil
}

func (m *WDAManager) installIPA(udid, kind, ipa, iosVersion string) error {
	bin, args, err := m.installCmd(udid, kind, ipa, iosVersion)
	if err != nil {
		return err
	}
	slog.Info("install WDA IPA", "kind", kind, "ipa", ipa, "udid", shortOf(udid))
	if _, err := runTool(bin, args, 2*time.Minute); err != nil {
		return fmt.Errorf("安装 IPA 失败（%s）：%w", kind, err)
	}
	slog.Info("WDA IPA installed", "udid", shortOf(udid))
	return nil
}

func runTool(bin string, args []string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), bundleLibFallback()...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if len(text) > 800 {
			text = text[:800] + "…"
		}
		if text == "" {
			return "", err
		}
		return text, fmt.Errorf("%w: %s", err, text)
	}
	return text, nil
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

func verOrUnknown(v string) string {
	if strings.TrimSpace(v) == "" {
		return "未知版本"
	}
	return v
}

func (m *WDAManager) ddiCacheDir() string {
	if m != nil && strings.TrimSpace(m.derivedData) != "" {
		return filepath.Join(filepath.Dir(m.derivedData), "ddi")
	}
	return filepath.Join(os.TempDir(), "wda-ddi")
}

// prepareDeviceForIOS 按系统版本补齐激活前置：DDI / go-ios 镜像、iOS 17+ 隧道、开发者模式。
func (m *WDAManager) prepareDeviceForIOS(udid, kind, ver string, plan iosSupportPlan) error {
	if plan.NeedDDI {
		ddiErr := EnsureDeviceSupportDDI(udid)
		if ddiErr != nil {
			slog.Warn("EnsureDeviceSupportDDI failed", "udid", shortOf(udid), "error", ddiErr)
		}
		// Windows 没有 Xcode DeviceSupport；Mac 上 Xcode 镜像失败时再用 go-ios 下载挂载。
		if kind == activatorGoIOS && (runtime.GOOS != "darwin" || ddiErr != nil) {
			if err := goiosImageAuto(udid, m.ddiCacheDir()); err != nil {
				if runtime.GOOS != "darwin" {
					return fmt.Errorf("iOS %s 需要开发者镜像（ios image auto）：%w", verOrUnknown(ver), err)
				}
				slog.Warn("ios image auto failed", "udid", shortOf(udid), "error", err)
			}
		}
	}

	if plan.NeedTunnel || likelyNeedsTunnel(udid, ver) {
		if err := goiosTunnel.Ensure(); err != nil {
			if plan.NeedTunnel {
				return err
			}
			slog.Warn("go-ios tunnel start skipped (version unknown)", "udid", shortOf(udid), "error", err)
		} else if plan.NeedTunnel {
			if err := goiosTunnel.WaitDevice(udid, 45*time.Second); err != nil {
				if !plan.UserspaceOK {
					return fmt.Errorf("iOS %s 的用户态隧道不受 go-ios 支持（需 17.4+）。请升级系统，或先用管理员执行 ios tunnel start（Windows 还需 wintun.dll）。原因：%w", ver, err)
				}
				return err
			}
		}
	}

	if plan.NeedDevMode {
		if err := ensureDeveloperMode(udid); err != nil {
			return err
		}
	}
	return nil
}

func goiosImageAutoArgs(udid, basedir string) []string {
	args := []string{"--udid=" + udid, "image", "auto"}
	if basedir != "" {
		args = append(args, "--basedir="+basedir)
	}
	return args
}

func goiosImageAuto(udid, basedir string) error {
	bin := lookTool("ios", "ios.exe")
	if bin == "" {
		return fmt.Errorf("未找到 go-ios（ios）")
	}
	if basedir != "" {
		if err := os.MkdirAll(basedir, 0o755); err != nil {
			return fmt.Errorf("创建开发者镜像目录：%w", err)
		}
	}
	slog.Info("ios image auto", "udid", shortOf(udid), "basedir", basedir)
	if _, err := runTool(bin, goiosImageAutoArgs(udid, basedir), 3*time.Minute); err != nil {
		return err
	}
	return nil
}

func ensureDeveloperMode(udid string) error {
	bin := lookTool("ios", "ios.exe")
	if bin == "" {
		return nil
	}
	out, err := runTool(bin, []string{"--udid=" + udid, "--pretty", "devmode", "get"}, 20*time.Second)
	if err != nil {
		slog.Warn("devmode get failed", "udid", shortOf(udid), "error", err)
		return nil
	}
	enabled, ok := parseDevModeEnabled(out)
	if ok && !enabled {
		return fmt.Errorf("iOS 16+ 未打开开发者模式。请到「设置 → 隐私与安全性 → 开发者模式」打开并重启手机后再激活")
	}
	return nil
}

// pingArgs 探测 IPv4 可达性。Windows 的 ping 开关与 Unix 不同。
func pingArgs(ip string) []string {
	if runtime.GOOS == "windows" {
		return []string{"-n", "1", "-w", "2000", ip}
	}
	return []string{"-c", "1", "-t", "2", ip}
}
