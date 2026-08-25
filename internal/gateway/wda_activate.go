package gateway

import (
	"context"
	"errors"
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
	// defaultWifiNetworkWait 手动 Network 激活等待 usbmux Network 条目的窗口。
	defaultWifiNetworkWait = 30 * time.Second

	activateViaUSB     = "usb"
	activateViaNetwork = "network"

	// wifiLockdownTimeout 覆盖 wifi-lockdown 默认 60s 等手机输锁屏密码。
	wifiLockdownTimeout = 75 * time.Second
)

// errDevicePasscodeNeeded 写 EnableWifiDebugging 时 iPhone 弹出锁屏密码框，用户尚未在手机上输入。
var errDevicePasscodeNeeded = errors.New("请看 iPhone：若弹出锁屏密码框，请在手机上输入以开启无线调试。若尚未设置锁屏密码，先到「设置 → 面容 ID 与密码 / 触控 ID 与密码」设置 4 位数字密码。密码只在手机上输入，不要发给电脑")

// errNeedWifiAuth Network 激活前必须先经 USB 完成首次授权。
var errNeedWifiAuth = errors.New("请先插 USB，在手机上设置锁屏密码后点「首次授权」，才能开启无线调试并使用 Network")

// errWifiAuthNeedUSB 写 EnableWifiConnections/Debugging 必须走 USB，不能走 Network。
var errWifiAuthNeedUSB = errors.New("首次授权必须连接 USB 线，并先在手机上设置锁屏密码，才能开启 EnableWifiConnections/EnableWifiDebugging")

// parseActivateVia 只接受 usb / network；空或其它值一律当 usb（自动激活默认）。
func parseActivateVia(s string) string {
	if strings.EqualFold(strings.TrimSpace(s), activateViaNetwork) {
		return activateViaNetwork
	}
	return activateViaUSB
}

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

// enableWifiLockdown 仅 USB 下打开 EnableWifiConnections / EnableWifiDebugging。
// 必须已插 USB，且手机已设锁屏密码；写 Debugging 时手机再弹密码框。密码不进本机。
func enableWifiLockdown(udid string) error {
	return enableWifiLockdownOn(udid, usbPresent(udid))
}

func enableWifiLockdownOn(udid string, usb bool) error {
	if udid == "" {
		return fmt.Errorf("缺少 UDID")
	}
	if !usb {
		return errWifiAuthNeedUSB
	}
	bin := lookTool("wifi-lockdown", "wifi-lockdown.exe")
	if bin == "" {
		return fmt.Errorf("未找到 wifi-lockdown，无法开启 EnableWifiConnections/EnableWifiDebugging")
	}
	out, err := runTool(bin, []string{udid}, wifiLockdownTimeout)
	if err != nil {
		if isDevicePasscodeOutput(err.Error(), out) {
			slog.Warn("wifi lockdown waiting for device passcode", "udid", shortOf(udid))
			return errDevicePasscodeNeeded
		}
		slog.Warn("enable wifi lockdown failed", "udid", shortOf(udid), "error", err)
		return fmt.Errorf("开启无线调试失败：%w", err)
	}
	// 给 usbmuxd 一点时间挂上 Network 条目；真正的等待在 wifi-runwda -wait-network。
	time.Sleep(2 * time.Second)
	slog.Info("wifi lockdown enabled", "udid", shortOf(udid))
	return nil
}

func isDevicePasscodeOutput(parts ...string) bool {
	s := strings.Join(parts, "\n")
	return strings.Contains(s, "NEED_DEVICE_PASSCODE") ||
		strings.Contains(s, "PasscodeRequired") ||
		strings.Contains(s, "PasswordProtected") ||
		strings.Contains(s, "0xe80000ee") ||
		strings.Contains(s, "e80000ee")
}

func isDevicePasscodeErr(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, errDevicePasscodeNeeded) || isDevicePasscodeOutput(err.Error())
}

func hasMuxNetwork(udid string) bool {
	// Windows 上 idevice_id 连 AMDS 看不到 netmuxd 的无线设备，必须用 go-ios
	// （读 USBMUXD_SOCKET_ADDRESS）的 ios list 结果；Mac 直接用 NetworkUDIDs()。
	if runtime.GOOS == "windows" {
		if m := usbmuxNetworkUDIDs(); m[udidKey(udid)] {
			return true
		}
	}
	for _, u := range NetworkUDIDs() {
		if strings.EqualFold(u, udid) {
			return true
		}
	}
	return false
}

func parseWifiLockdownStatus(raw string) (connections, debugging bool) {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.Contains(line, "EnableWifiConnections="):
			connections = wifiLockdownValueTrue(line)
		case strings.Contains(line, "EnableWifiDebugging="):
			debugging = wifiLockdownValueTrue(line)
		}
	}
	return connections, debugging
}

func wifiLockdownValueTrue(line string) bool {
	i := strings.LastIndex(line, "=")
	if i < 0 {
		return false
	}
	v := strings.TrimSpace(line[i+1:])
	return strings.EqualFold(v, "true") || v == "1"
}

// wifiDebugReady 只认 USB 首次授权留下的标记，不把 usbmux Network 当成已授权。
func wifiDebugReady(udid string, flagged bool) bool {
	_ = udid
	return flagged
}

func needWifiAuth(usbAttached, authorized bool) bool {
	return usbAttached && !authorized
}

func (m *WDAManager) wdaBundleID() string {
	if m != nil && strings.TrimSpace(m.bundleID) != "" {
		return strings.TrimSpace(m.bundleID)
	}
	return defaultWDABundleID
}

func usbPresent(udid string) bool {
	for _, u := range USBUDIDs() {
		if strings.EqualFold(u, udid) {
			return true
		}
	}
	return false
}

func userStopped(d *Device, running bool) bool {
	return d != nil && !d.AutoReactivate && !running
}

func tunnelVias(devs []Device) map[string]string {
	out := map[string]string{}
	for _, d := range devs {
		if d.UDID == "" {
			continue
		}
		out[normalizeUDID(d.UDID)] = parseActivateVia(d.ActivateVia)
	}
	return out
}

func (m *WDAManager) activateProtocol(udid string, port int, reportedUDID, kind, via string) error {
	via = parseActivateVia(via)
	if via == activateViaUSB && !usbPresent(udid) {
		return fmt.Errorf("USB 激活需要 USB 连接，不会回退 Network")
	}
	ver := resolveIOSVersion(udid, "")
	plan := planForIOS(ver)
	slog.Info("ios activate plan", "udid", shortOf(udid), "ios", ver, "via", via, "major", plan.Major,
		"ddi", plan.NeedDDI, "tunnel", plan.NeedTunnel, "devmode", plan.NeedDevMode, "userspace_ok", plan.UserspaceOK)

	if plan.NeedTunnel && kind != activatorGoIOS {
		if lookTool("ios", "ios.exe") != "" {
			slog.Info("iOS 17+ switching activator to go-ios", "was", kind, "udid", shortOf(udid))
			kind = activatorGoIOS
		} else {
			return fmt.Errorf("iOS %s 需要 go-ios 隧道（ios tunnel start），未找到 ios/ios.exe", verOrUnknown(ver))
		}
	}

	// 两个无线开关只允许 USB 首次授权写入；Network 激活不再写、不兜底。
	if err := m.prepareDeviceForIOS(udid, kind, ver, plan); err != nil {
		return err
	}
	if err := m.ensureRunnerInstalled(udid, kind, ver); err != nil {
		return err
	}
	bin, args, err := m.protocolCmd(udid, port, reportedUDID, kind, ver, via)
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

func (m *WDAManager) protocolCmd(udid string, port int, reportedUDID, kind, iosVersion, via string) (string, []string, error) {
	via = parseActivateVia(via)
	bundle := m.wdaBundleID()
	switch kind {
	case activatorGoIOS:
		if via == activateViaNetwork {
			if needsRemoteXPCTunnel(iosVersion) {
				return "", nil, fmt.Errorf("iOS %s 的 Network 激活不能走 go-ios 隧道（那是 USB/CoreDevice 通道，不会回退）", verOrUnknown(iosVersion))
			}
			// Windows：netmuxd 已提供 ConnectionType=Network 条目，直接 ios runwda 走无线
			// testmanagerd（wifi-runwda 的双层 usbmux 代理在 netmuxd 下会中止连接）。
			if runtime.GOOS == "windows" && os.Getenv("USBMUXD_SOCKET_ADDRESS") != "" {
				bin := lookTool("ios", "ios.exe")
				if bin == "" {
					return "", nil, fmt.Errorf("未找到 go-ios（ios）：请把它放到 PATH 或 WDA_GATEWAY_RESOURCES/bin")
				}
				slog.Info("activate via network (netmuxd)", "udid", shortOf(udid), "ios", iosVersion)
				return bin, goiosArgs(udid, bundle, port, reportedUDID), nil
			}
			if bin, args, ok := wifiRunwdaInvocation(udid, port, bundle, true); ok {
				slog.Info("activate via network", "udid", shortOf(udid), "ios", iosVersion)
				return bin, args, nil
			}
			return "", nil, fmt.Errorf("未找到 wifi-runwda：iOS %s 的 Network 激活需要它（不会回退 USB）", verOrUnknown(iosVersion))
		}
		// USB：iOS 17+ 走 go-ios RSD 隧道；iOS ≤16 走 ios runwda。都不走 wifi-runwda。
		bin := lookTool("ios", "ios.exe")
		if bin == "" {
			return "", nil, fmt.Errorf("未找到 go-ios（ios）：请把它放到 PATH 或 WDA_GATEWAY_RESOURCES/bin")
		}
		args := goiosArgs(udid, bundle, port, reportedUDID)
		if needsRemoteXPCTunnel(iosVersion) {
			args = withGoIOSTunnelPort(args)
		}
		slog.Info("activate via usb", "udid", shortOf(udid), "ios", iosVersion)
		return bin, args, nil
	case activatorTidevice:
		if via == activateViaNetwork {
			return "", nil, fmt.Errorf("Network 激活不能走 tidevice（tidevice 走 USB，不会回退）")
		}
		bin := lookTool("tidevice", "tidevice.exe")
		if bin == "" {
			return "", nil, fmt.Errorf("未找到 tidevice：请安装后放到 PATH 或 WDA_GATEWAY_RESOURCES/bin")
		}
		return bin, tideviceArgs(udid, bundle, port, reportedUDID), nil
	default:
		return "", nil, fmt.Errorf("未知激活后端 %q", kind)
	}
}

// wifiRunwdaInvocation 仅用于手动 Network 激活：必须等 usbmux Network，禁止 USB 回退。
// 不传 -ip，避免抢连 :62078 把已有 Network 行拆掉。
func wifiRunwdaInvocation(udid string, port int, bundleID string, requireNetwork bool) (string, []string, bool) {
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
		"-wait-network", defaultWifiNetworkWait.String(),
	}
	if requireNetwork {
		args = append(args, "-require-network")
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
		return p
	}
	for _, dir := range repoToolsDirs() {
		cand := filepath.Join(dir, "wda.ipa")
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
	if needsDeveloperDiskImage(iosVersion) && runtime.GOOS != "darwin" && kind == activatorGoIOS {
		if err := goiosImageAuto(udid, m.ddiCacheDir()); err != nil {
			return fmt.Errorf("iOS %s 需要开发者镜像（ios image auto）：%w", verOrUnknown(iosVersion), err)
		}
	}
	m.installSigningProfile(udid, kind, iosVersion, ipa)
	return m.installIPA(udid, kind, ipa, iosVersion)
}

func (m *WDAManager) runnerInstalled(udid, kind, iosVersion string) (bool, error) {
	bin, args, err := m.listAppsCmd(udid, kind, iosVersion)
	if err != nil {
		return false, err
	}
	out, err := runTool(bin, args, 8*time.Second)
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
	} else {
		// 源码运行：用仓库 tools/，这样别人 git pull 后不必再配 PATH。
		// 桌面壳会设 WDA_GATEWAY_RESOURCES，测试缺二进制时也设它，避免误用仓内工具。
		candidates = append(candidates, repoToolPaths(names)...)
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

func repoToolsDirs() []string {
	var starts []string
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	if ex, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(ex))
	}
	var dirs []string
	seen := map[string]bool{}
	for _, start := range starts {
		dir := start
		for i := 0; i < 6; i++ {
			cand := filepath.Join(dir, "tools")
			if st, err := os.Stat(cand); err == nil && st.IsDir() && !seen[cand] {
				seen[cand] = true
				dirs = append(dirs, cand)
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return dirs
}

func repoToolPaths(names []string) []string {
	var out []string
	for _, dir := range repoToolsDirs() {
		for _, name := range names {
			out = append(out, filepath.Join(dir, name))
		}
	}
	return out
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

// prepareDeviceForIOS USB 激活前置：只做当前版本真正挡住 runwda 的步骤。
func (m *WDAManager) prepareDeviceForIOS(udid, kind, ver string, plan iosSupportPlan) error {
	if plan.NeedDDI && runtime.GOOS == "darwin" {
		if err := EnsureDeviceSupportDDI(udid); err != nil {
			slog.Warn("EnsureDeviceSupportDDI failed", "udid", shortOf(udid), "error", err)
		}
	}

	if plan.NeedTunnel {
		if err := goiosTunnel.Ensure(); err != nil {
			return err
		}
		if err := goiosTunnel.WaitDevice(udid, 20*time.Second); err != nil {
			if !plan.UserspaceOK {
				return fmt.Errorf("iOS %s 的用户态隧道不受 go-ios 支持（需 17.4+）。请升级系统，或先用管理员执行 ios tunnel start（Windows 还需 wintun.dll）。原因：%w", ver, err)
			}
			return err
		}
	}

	if plan.NeedDevMode {
		if err := ensureDeveloperMode(udid); err != nil {
			return err
		}
	}
	_ = kind
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
	out, err := runTool(bin, []string{"--udid=" + udid, "--pretty", "devmode", "get"}, 5*time.Second)
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
