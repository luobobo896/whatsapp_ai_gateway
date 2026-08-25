package gateway

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFindExistingXctestrunPrefersTemplate(t *testing.T) {
	dir := t.TempDir()
	prod := filepath.Join(dir, "Build", "Products")
	if err := os.MkdirAll(prod, 0o755); err != nil {
		t.Fatal(err)
	}
	template := filepath.Join(prod, "WebDriverAgentRunner_iphoneos18.5-arm64.xctestrun")
	runtimeFile := template + ".59524996.runtime.xctestrun"
	if err := os.WriteFile(template, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeFile, []byte("rt"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := findExistingXctestrun(dir)
	if got != template {
		t.Fatalf("got %q want template %q", got, template)
	}
}

func TestFindExistingXctestrunEmpty(t *testing.T) {
	if got := findExistingXctestrun(t.TempDir()); got != "" {
		t.Fatalf("empty derived want \"\", got %q", got)
	}
}

func TestResolveActivator(t *testing.T) {
	if got := resolveActivator("goios"); got != activatorGoIOS {
		t.Fatalf("explicit goios: got %q", got)
	}
	if got := resolveActivator("tidevice"); got != activatorTidevice {
		t.Fatalf("explicit tidevice: got %q", got)
	}
	if got := resolveActivator("xcodebuild"); got != activatorXcodebuild {
		t.Fatalf("explicit xcodebuild: got %q", got)
	}
	auto := resolveActivator("auto")
	empty := resolveActivator("")
	if auto != empty {
		t.Fatalf("auto vs empty: %q vs %q", auto, empty)
	}
	want := autoActivator(lookTool("ios", "ios.exe") != "", lookTool("tidevice", "tidevice.exe") != "")
	if auto != want {
		t.Fatalf("auto: got %q want %q", auto, want)
	}
}

func TestAutoActivator(t *testing.T) {
	if got := autoActivator(true, true); got != activatorGoIOS {
		t.Fatalf("prefer goios when both exist: %q", got)
	}
	if got := autoActivator(false, true); got != activatorTidevice {
		t.Fatalf("tidevice when no goios: %q", got)
	}
	if got := autoActivatorFor(false, false, "windows"); got != activatorGoIOS {
		t.Fatalf("windows no tools want goios, got %q", got)
	}
	if got := autoActivatorFor(false, false, "darwin"); got != activatorXcodebuild {
		t.Fatalf("mac no tools want xcodebuild, got %q", got)
	}
}

func TestGoiosArgs(t *testing.T) {
	udid := "00008120-000865D90A10C01E"
	args := goiosArgs(udid, defaultWDABundleID, 8100, "")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--udid=" + udid,
		"runwda",
		"--bundleid=" + defaultWDABundleID,
		"--testrunnerbundleid=" + defaultWDABundleID,
		"--xctestconfig=" + defaultXCTestConfig,
		"--env=USE_PORT=8100",
		"--env=WDA_DEVICE_UDID=" + udid,
	} {
		if !containsExact(args, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
}

func TestTideviceArgs(t *testing.T) {
	udid := "5060c403af00112233445566778899aabbccddee"
	args := tideviceArgs(udid, "com.wda.WebRunner.xctrunner", 8200, "reported-udid")
	want := []string{
		"-u", udid, "xctest", "-B", "com.wda.WebRunner.xctrunner",
		"-e", "USE_PORT:8200", "-e", "WDA_DEVICE_UDID:reported-udid",
	}
	if len(args) != len(want) {
		t.Fatalf("len=%d want %d: %#v", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d]=%q want %q", i, args[i], want[i])
		}
	}
}

func TestLookToolFindsRepoToolsWhenResourcesUnset(t *testing.T) {
	t.Setenv("PATH", "/nonexistent")
	t.Setenv("WDA_GATEWAY_RESOURCES", "")
	if _, err := os.Stat("tools/ios"); err != nil {
		t.Skip("run from repo root so tools/ios is visible")
	}
	if p := lookTool("ios", "ios.exe"); p == "" {
		t.Fatal("expected tools/ios when WDA_GATEWAY_RESOURCES is unset")
	}
}

func TestResolvedIPAFallsBackToRepoTools(t *testing.T) {
	t.Setenv("WDA_GATEWAY_RESOURCES", "")
	m := NewWDAManager("", "", "")
	m.ipaPath = filepath.Join(t.TempDir(), "missing.ipa")
	got := m.resolvedIPA()
	if got == "" {
		t.Skip("tools/wda.ipa not present")
	}
	if filepath.Base(got) != "wda.ipa" {
		t.Fatalf("got %q", got)
	}
}

func TestEnableWifiLockdownMissingBinaryErrors(t *testing.T) {
	t.Setenv("PATH", "/nonexistent")
	t.Setenv("WDA_GATEWAY_RESOURCES", "/nonexistent-resources")
	if err := enableWifiLockdownOn("00008120-000865D90A10C01E", true); err == nil {
		t.Fatal("missing wifi-lockdown must fail")
	}
}

func TestParseWifiLockdownStatus(t *testing.T) {
	c, d := parseWifiLockdownStatus("" +
		"5060c403afdee4c15a0edeab69dba0524e2ce592 EnableWifiConnections=true\n" +
		"5060c403afdee4c15a0edeab69dba0524e2ce592 EnableWifiDebugging=true\n")
	if !c || !d {
		t.Fatalf("both true, got %v %v", c, d)
	}
	c, d = parseWifiLockdownStatus("EnableWifiConnections=true\nEnableWifiDebugging=false\n")
	if !c || d {
		t.Fatalf("debug false, got %v %v", c, d)
	}
}

func TestRequirePasscode0000(t *testing.T) {
	if err := requirePasscode0000("0000"); err != nil {
		t.Fatalf("0000 must pass, got %v", err)
	}
	for _, bad := range []string{"", "1234", "000", "00000", "abcd"} {
		if err := requirePasscode0000(bad); err == nil {
			t.Fatalf("must reject passcode %q", bad)
		}
	}
}

func TestAlreadyWifiAuthorized(t *testing.T) {
	if !alreadyWifiAuthorized(true, true, true) {
		t.Fatal("both flags true with ok status must be authorized")
	}
	if alreadyWifiAuthorized(true, false, true) {
		t.Fatal("debugging false must not skip")
	}
	if alreadyWifiAuthorized(false, true, true) {
		t.Fatal("connections false must not skip")
	}
	if alreadyWifiAuthorized(true, true, false) {
		t.Fatal("status read failure must not skip (fall back to write)")
	}
}

func TestNeedWifiAuth(t *testing.T) {
	if !needWifiAuth(true, false) {
		t.Fatal("USB first connect without wifi debug needs auth")
	}
	if needWifiAuth(true, true) {
		t.Fatal("already authorized")
	}
	if needWifiAuth(false, false) {
		t.Fatal("no USB is not first-connect auth")
	}
}

func TestWifiDebugReadyOnlyFlag(t *testing.T) {
	if !wifiDebugReady("no-such-udid", true) {
		t.Fatal("USB authorize flag is the only ready signal")
	}
	if wifiDebugReady("no-such-udid", false) {
		t.Fatal("Network row or live status must not skip USB authorize")
	}
}

func TestWifiAuthorizedFlagBypassesLiveProbe(t *testing.T) {
	// USB 首次授权标记本身就足够，不触发 hasMuxNetwork 的实时探测。
	if !wifiAuthorized("no-such-udid", true) {
		t.Fatal("wifiAuthorized must accept USB 授权标记")
	}
}

func TestEnableWifiLockdownRequiresUSB(t *testing.T) {
	err := enableWifiLockdownOn("ffffffffffffffffffffffffffffffffffffffff", false)
	if !errors.Is(err, errWifiAuthNeedUSB) {
		t.Fatalf("got %v", err)
	}
}

func TestIsDevicePasscodeOutput(t *testing.T) {
	if !isDevicePasscodeOutput("set EnableWifiDebugging: Failed ... PasscodeRequired") {
		t.Fatal("PasscodeRequired")
	}
	if !isDevicePasscodeOutput("NEED_DEVICE_PASSCODE\n请在 iPhone 上输入") {
		t.Fatal("token")
	}
	if isDevicePasscodeOutput("get device: not found") {
		t.Fatal("other")
	}
	if isDevicePasscodeErr(nil) {
		t.Fatal("nil")
	}
	if !isDevicePasscodeErr(errDevicePasscodeNeeded) {
		t.Fatal("sentinel")
	}
	if !isDevicePasscodeErr(errors.Join(errDevicePasscodeNeeded)) {
		t.Fatal("wrapped sentinel")
	}
	if strings.Contains(errDevicePasscodeNeeded.Error(), "0000") || strings.Contains(errNeedWifiAuth.Error(), "0000") || strings.Contains(errWifiAuthNeedUSB.Error(), "0000") {
		t.Fatal("must not mention a sample passcode")
	}
}

func TestEnableWifiLockdownPasscodeError(t *testing.T) {
	dir := t.TempDir()
	name := "wifi-lockdown"
	var script string
	if runtime.GOOS == "windows" {
		// Windows 的 LookPath 只认 PATHEXT（.exe/.cmd 等），且 CreateProcess 可直接跑 .cmd。
		name = "wifi-lockdown.cmd"
		script = "@echo off\r\necho set EnableWifiDebugging: Failed setting with err: PasscodeRequired 1>&2\r\nexit /b 3\r\n"
	} else {
		script = "#!/bin/sh\necho 'set EnableWifiDebugging: Failed setting with err: PasscodeRequired' >&2\nexit 3\n"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("WDA_GATEWAY_RESOURCES", filepath.Join(dir, "no-resources"))
	err := enableWifiLockdownOn("5060c403afdee4c15a0edeab69dba0524e2ce592", true)
	if !errors.Is(err, errDevicePasscodeNeeded) {
		t.Fatalf("got %v", err)
	}
}

func TestProtocolCmdMissingBinary(t *testing.T) {
	m := NewWDAManager("", "", "")
	t.Setenv("PATH", "/nonexistent")
	t.Setenv("WDA_GATEWAY_RESOURCES", "/nonexistent-resources")
	if _, _, err := m.protocolCmd("00008120-000865D90A10C01E", 8100, "", activatorGoIOS, "18.6", activateViaUSB); err == nil {
		t.Fatal("expected error when ios binary is missing")
	}
	if _, _, err := m.protocolCmd("00008120-000865D90A10C01E", 8100, "", activatorTidevice, "15.8.7", activateViaUSB); err == nil {
		t.Fatal("expected error when tidevice binary is missing")
	}
}

func TestWifiRunwdaInvocationMissingBinary(t *testing.T) {
	t.Setenv("PATH", "/nonexistent")
	t.Setenv("WDA_GATEWAY_RESOURCES", "/nonexistent-resources")
	if _, _, ok := wifiRunwdaInvocation("00008120-000865D90A10C01E", 8100, defaultWDABundleID, true); ok {
		t.Fatal("missing wifi-runwda must not be selected")
	}
}

func TestWifiRunwdaInvocationBuildsArgs(t *testing.T) {
	dir := plantActivateTools(t, true, true)
	t.Setenv("PATH", dir)
	t.Setenv("WDA_GATEWAY_RESOURCES", filepath.Join(dir, "no-resources"))
	udid := "4886579a97a96bad83b527862bab409b5a07c741"
	bin, args, ok := wifiRunwdaInvocation(udid, 8100, defaultWDABundleID, true)
	if !ok {
		t.Fatal("expected wifi-runwda when both binaries resolve")
	}
	if bin == "" {
		t.Fatal("empty wrapper path")
	}
	for _, want := range []string{"-udid", udid, "-port", "8100", "-bundle", defaultWDABundleID, "-ios", "-wait-network", "30s", "-require-network"} {
		if !containsExact(args, want) {
			t.Fatalf("missing %q in %#v", want, args)
		}
	}
	if containsExact(args, "-ip") {
		t.Fatalf("Network activate must not probe :62078, got %#v", args)
	}
}

func TestParseActivateVia(t *testing.T) {
	if parseActivateVia("") != activateViaUSB || parseActivateVia("USB") != activateViaUSB {
		t.Fatal("empty/USB must be usb")
	}
	if parseActivateVia("network") != activateViaNetwork || parseActivateVia("Network") != activateViaNetwork {
		t.Fatal("network must be network")
	}
}

func TestProtocolCmdIOS15USBUsesGoIOS(t *testing.T) {
	dir := plantActivateTools(t, true, true)
	t.Setenv("PATH", dir)
	t.Setenv("WDA_GATEWAY_RESOURCES", filepath.Join(dir, "no-resources"))
	m := NewWDAManager("", "", "")
	udid := "4886579a97a96bad83b527862bab409b5a07c741"
	bin, args, err := m.protocolCmd(udid, 8100, udid, activatorGoIOS, "15.8.7", activateViaUSB)
	if err != nil {
		t.Fatal(err)
	}
	if baseTool(bin) != "ios" {
		t.Fatalf("USB activate must use ios runwda, bin=%q", bin)
	}
	if !containsExact(args, "runwda") {
		t.Fatalf("args=%#v", args)
	}
	if containsExact(args, "-require-network") {
		t.Fatalf("USB path must not require Network: %#v", args)
	}
}

func TestProtocolCmdIOS15NetworkUsesWifiRunwda(t *testing.T) {
	dir := plantActivateTools(t, true, true)
	t.Setenv("PATH", dir)
	t.Setenv("WDA_GATEWAY_RESOURCES", filepath.Join(dir, "no-resources"))
	m := NewWDAManager("", "", "")
	udid := "4886579a97a96bad83b527862bab409b5a07c741"
	bin, args, err := m.protocolCmd(udid, 8100, udid, activatorGoIOS, "15.8.7", activateViaNetwork)
	if err != nil {
		t.Fatal(err)
	}
	if baseTool(bin) != "wifi-runwda" {
		t.Fatalf("bin=%q want wifi-runwda", bin)
	}
	if !containsExact(args, "-require-network") || !containsExact(args, "30s") {
		t.Fatalf("Network activate must require usbmux Network: %#v", args)
	}
}

// TestProtocolCmdWindowsNetmuxdNetworkUsesGoIOS：Windows 上 netmuxd 提供
// Network 条目后，Network 激活直接走 ios runwda（不再套 wifi-runwda 代理）。
func TestProtocolCmdWindowsNetmuxdNetworkUsesGoIOS(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows netmuxd 路径")
	}
	t.Setenv("USBMUXD_SOCKET_ADDRESS", "127.0.0.1:27016")
	t.Setenv("WDA_GATEWAY_RESOURCES", "")
	m := NewWDAManager("", "", "")
	udid := "4886579a97a96bad83b527862bab409b5a07c741"
	bin, args, err := m.protocolCmd(udid, 8100, udid, activatorGoIOS, "15.8.8", activateViaNetwork)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(bin) != "ios.exe" {
		t.Fatalf("bin=%q, want ios.exe", bin)
	}
	if !containsExact(args, "runwda") {
		t.Fatalf("args missing runwda: %#v", args)
	}
}

func TestProtocolCmdIOS15NetworkRequiresWifiRunwda(t *testing.T) {
	dir := plantActivateTools(t, false, true)
	t.Setenv("PATH", dir)
	t.Setenv("WDA_GATEWAY_RESOURCES", filepath.Join(dir, "no-resources"))
	m := NewWDAManager("", "", "")
	_, _, err := m.protocolCmd("5060c403afdee4c15a0edeab69dba0524e2ce592", 8100, "", activatorGoIOS, "15.8.7", activateViaNetwork)
	if err == nil {
		t.Fatal("iOS 15 Network activate without wifi-runwda must fail")
	}
	if !strings.Contains(err.Error(), "wifi-runwda") {
		t.Fatalf("error=%v", err)
	}
}

func TestProtocolCmdIOS15USBWorksWithoutWifiRunwda(t *testing.T) {
	dir := plantActivateTools(t, false, true)
	t.Setenv("PATH", dir)
	t.Setenv("WDA_GATEWAY_RESOURCES", filepath.Join(dir, "no-resources"))
	m := NewWDAManager("", "", "")
	bin, args, err := m.protocolCmd("5060c403afdee4c15a0edeab69dba0524e2ce592", 8100, "", activatorGoIOS, "15.8.7", activateViaUSB)
	if err != nil {
		t.Fatal(err)
	}
	if baseTool(bin) != "ios" || !containsExact(args, "runwda") {
		t.Fatalf("USB activate should be ios runwda: bin=%q args=%#v", bin, args)
	}
}

func TestProtocolCmdGoIOSWithoutWrapper(t *testing.T) {
	dir := plantActivateTools(t, false, true)
	t.Setenv("PATH", dir)
	t.Setenv("WDA_GATEWAY_RESOURCES", filepath.Join(dir, "no-resources"))
	m := NewWDAManager("", "", "")
	udid := "00008120-000865D90A10C01E"
	bin, args, err := m.protocolCmd(udid, 8100, "", activatorGoIOS, "18.6", activateViaUSB)
	if err != nil {
		t.Fatal(err)
	}
	if baseTool(bin) != "ios" {
		t.Fatalf("bin=%q want ios", bin)
	}
	if !containsExact(args, "runwda") || !containsExact(args, "--tunnel-info-port=28100") {
		t.Fatalf("iOS 17+ without wrapper must still use tunnel runwda: %#v", args)
	}
}

// baseTool 返回可执行文件基名（Windows 上去掉 .exe，便于与 unix 名称断言对齐）。
func baseTool(p string) string {
	return strings.TrimSuffix(filepath.Base(p), ".exe")
}

func plantActivateTools(t *testing.T, wrap, ios bool) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nexit 0\n"
	exe := func(name string) string {
		if runtime.GOOS == "windows" {
			return name + ".exe"
		}
		return name
	}
	if wrap {
		if err := os.WriteFile(filepath.Join(dir, exe("wifi-runwda")), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if ios {
		if err := os.WriteFile(filepath.Join(dir, exe("ios")), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestProtocolCmdIOS17NetworkRefusesUSBTunnel(t *testing.T) {
	dir := plantActivateTools(t, true, true)
	t.Setenv("PATH", dir)
	t.Setenv("WDA_GATEWAY_RESOURCES", filepath.Join(dir, "no-resources"))
	m := NewWDAManager("", "", "")
	udid := "00008120-000865D90A10C01E"
	_, _, err := m.protocolCmd(udid, 8100, udid, activatorGoIOS, "18.6", activateViaNetwork)
	if err == nil || !strings.Contains(err.Error(), "不会回退") {
		t.Fatalf("iOS 17+ Network must not use USB tunnel, err=%v", err)
	}
}

func TestProtocolCmdIOS17USBUsesGoIOS(t *testing.T) {
	dir := plantActivateTools(t, true, true)
	t.Setenv("PATH", dir)
	t.Setenv("WDA_GATEWAY_RESOURCES", filepath.Join(dir, "no-resources"))
	m := NewWDAManager("", "", "")
	udid := "00008120-000865D90A10C01E"
	bin, args, err := m.protocolCmd(udid, 8100, udid, activatorGoIOS, "18.6", activateViaUSB)
	if err != nil {
		t.Fatal(err)
	}
	if baseTool(bin) == "wifi-runwda" {
		t.Fatal("USB via must not use wifi-runwda")
	}
	if baseTool(bin) != "ios" {
		t.Fatalf("bin=%q want ios", bin)
	}
	if !containsExact(args, "runwda") || !containsExact(args, "--tunnel-info-port=28100") {
		t.Fatalf("runwda tunnel args: %#v", args)
	}
}

func TestProtocolCmdTideviceNetworkRefused(t *testing.T) {
	dir := plantActivateTools(t, true, true)
	t.Setenv("PATH", dir)
	t.Setenv("WDA_GATEWAY_RESOURCES", filepath.Join(dir, "no-resources"))
	if err := os.WriteFile(filepath.Join(dir, "tidevice"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewWDAManager("", "", "")
	_, _, err := m.protocolCmd("5060c403afdee4c15a0edeab69dba0524e2ce592", 8100, "", activatorTidevice, "15.8.7", activateViaNetwork)
	if err == nil || !strings.Contains(err.Error(), "tidevice") {
		t.Fatalf("tidevice Network must fail, err=%v", err)
	}
}

func TestUserStopped(t *testing.T) {
	d := &Device{AutoReactivate: false}
	if !userStopped(d, false) {
		t.Fatal("manual stop must stick")
	}
	if userStopped(d, true) {
		t.Fatal("running process is not user-stopped")
	}
	d.AutoReactivate = true
	if userStopped(d, false) {
		t.Fatal("auto-reactivate is not user-stopped")
	}
}

func TestConnTypeOfDevice(t *testing.T) {
	if connTypeOfDevice(&Device{ActivateVia: activateViaNetwork}) != "wifi" {
		t.Fatal("network via reports wifi")
	}
	if connTypeOfDevice(&Device{ActivateVia: activateViaUSB}) != "usb" {
		t.Fatal("usb via reports usb")
	}
	if connTypeOfDevice(nil) != "usb" {
		t.Fatal("nil device defaults usb")
	}
}

func TestWdaProbeViaDoesNotCrossChannel(t *testing.T) {
	udid := "cafebabecafebabecafebabecafebabecafebabe"
	usbTunnels.mu.Lock()
	usbTunnels.procs[udid] = &usbTunnelProc{port: 1, done: make(chan struct{})}
	usbTunnels.mu.Unlock()
	t.Cleanup(func() {
		usbTunnels.mu.Lock()
		delete(usbTunnels.procs, udid)
		usbTunnels.mu.Unlock()
	})
	h := wdaProbeVia(udid, "192.168.1.8", 8100, activateViaNetwork)
	if h.OK {
		t.Fatal("network via must not use USB tunnel")
	}
	if !strings.Contains(h.Error, "Network") && h.Error == "" {
		t.Fatalf("want network-channel error, got %+v", h)
	}
}

func TestInstallCmdAddsTunnelPortOnIOS17(t *testing.T) {
	dir := plantActivateTools(t, false, true)
	t.Setenv("PATH", dir)
	t.Setenv("WDA_GATEWAY_RESOURCES", filepath.Join(dir, "no-resources"))
	m := NewWDAManager("", "", "")
	_, args, err := m.installCmd("00008120-000865D90A10C01E", activatorGoIOS, "/tmp/wda.ipa", "17.5")
	if err != nil {
		t.Fatal(err)
	}
	if !containsExact(args, "--tunnel-info-port=28100") || !containsExact(args, "install") {
		t.Fatalf("%v", args)
	}
	_, args16, err := m.installCmd("5060c403afdee4c15a0edeab69dba0524e2ce592", activatorGoIOS, "/tmp/wda.ipa", "15.8.7")
	if err != nil {
		t.Fatal(err)
	}
	if containsExact(args16, "--tunnel-info-port=28100") {
		t.Fatalf("iOS 15 install must not require tunnel port: %v", args16)
	}
}

func TestWDABundleIDDefault(t *testing.T) {
	m := NewWDAManager("", "", "")
	if m.wdaBundleID() != defaultWDABundleID {
		t.Fatalf("default bundle id: %q", m.wdaBundleID())
	}
	m.ConfigureSigning(SigningConfig{WDABundleID: "com.example.wda.xctrunner", Activator: "goios", IPAPath: "/tmp/wda.ipa"})
	if m.wdaBundleID() != "com.example.wda.xctrunner" {
		t.Fatalf("configured bundle id: %q", m.wdaBundleID())
	}
	if resolveActivator(m.activator) != activatorGoIOS {
		t.Fatalf("configured activator: %q", m.activator)
	}
	if m.ipaPath != "/tmp/wda.ipa" {
		t.Fatalf("configured ipa: %q", m.ipaPath)
	}
}

func TestGoiosInstallAndAppsArgs(t *testing.T) {
	udid := "00008120-000865D90A10C01E"
	ipa := `/data/wda.ipa`
	got := goiosInstallArgs(udid, ipa)
	want := []string{"--udid=" + udid, "install", "--path=" + ipa}
	if len(got) != len(want) {
		t.Fatalf("install args %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("install[%d]=%q want %q", i, got[i], want[i])
		}
	}
	apps := goiosAppsArgs(udid)
	if !containsExact(apps, "--udid="+udid) || !containsExact(apps, "apps") {
		t.Fatalf("apps args %#v", apps)
	}
}

func TestTideviceInstallAndAppsArgs(t *testing.T) {
	udid := "5060c403af00112233445566778899aabbccddee"
	ipa := `C:\wda\wda.ipa`
	got := tideviceInstallArgs(udid, ipa)
	want := []string{"-u", udid, "install", ipa}
	if len(got) != len(want) {
		t.Fatalf("install args %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("install[%d]=%q want %q", i, got[i], want[i])
		}
	}
	apps := tideviceAppsArgs(udid)
	if !containsExact(apps, "-u") || !containsExact(apps, udid) || !containsExact(apps, "applist") {
		t.Fatalf("applist args %#v", apps)
	}
}

func TestAppListContains(t *testing.T) {
	out := "com.apple.mobilesafari Safari 1.0\ncom.wda.WebRunner.xctrunner WebDriverAgentRunner-Runner 1.0\n"
	if !appListContains(out, defaultWDABundleID) {
		t.Fatal("should find runner")
	}
	if appListContains(out, "com.wda.Other") {
		t.Fatal("should not match other bundle")
	}
	if appListContains("", defaultWDABundleID) {
		t.Fatal("empty list is not installed")
	}
}

func TestResolvedIPAPrefersExistingFile(t *testing.T) {
	dir := t.TempDir()
	ipa := filepath.Join(dir, "wda.ipa")
	if err := os.WriteFile(ipa, []byte("ipa"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewWDAManager("", "", "")
	m.ConfigureSigning(SigningConfig{IPAPath: ipa})
	if got := m.resolvedIPA(); got != ipa {
		t.Fatalf("got %q want %q", got, ipa)
	}
}

func TestResolvedIPAFallsBackToResources(t *testing.T) {
	dir := t.TempDir()
	ipa := filepath.Join(dir, "wda.ipa")
	if err := os.WriteFile(ipa, []byte("ipa"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WDA_GATEWAY_RESOURCES", dir)
	m := NewWDAManager("", "", "")
	m.ipaPath = filepath.Join(dir, "missing.ipa")
	if got := m.resolvedIPA(); got != ipa {
		t.Fatalf("got %q want %q", got, ipa)
	}
}

func TestEnsureRunnerInstalledDefersWhenListFails(t *testing.T) {
	m := NewWDAManager("", "", "")
	m.ConfigureSigning(SigningConfig{Activator: "goios", IPAPath: filepath.Join(t.TempDir(), "no-such.ipa")})
	// 本机没有 ios 时 list 失败：不能断定「没装」，缺 IPA 也不硬失败，交给后续 start。
	t.Setenv("PATH", "/nonexistent")
	t.Setenv("WDA_GATEWAY_RESOURCES", "/nonexistent-resources")
	if err := m.ensureRunnerInstalled("00008120-000865D90A10C01E", activatorGoIOS, "18.0"); err != nil {
		t.Fatalf("list-failed + missing ipa should defer to start, got %v", err)
	}
}

func TestInstallCmdMissingBinary(t *testing.T) {
	m := NewWDAManager("", "", "")
	t.Setenv("PATH", "/nonexistent")
	t.Setenv("WDA_GATEWAY_RESOURCES", "/nonexistent-resources")
	if _, _, err := m.installCmd("00008120-000865D90A10C01E", activatorGoIOS, "/tmp/wda.ipa", "18.0"); err == nil {
		t.Fatal("expected error when ios binary is missing")
	}
	if _, _, err := m.installCmd("00008120-000865D90A10C01E", activatorTidevice, "/tmp/wda.ipa", "15.8.7"); err == nil {
		t.Fatal("expected error when tidevice binary is missing")
	}
}

func TestIdeviceProvisionArgs(t *testing.T) {
	udid := "5060c403afdee4c15a0edeab69dba0524e2ce592"
	got := ideviceProvisionInstallArgs(udid, "/tmp/a.mobileprovision")
	want := []string{"-u", udid, "install", "/tmp/a.mobileprovision"}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args[%d]=%q want %q", i, got[i], want[i])
		}
	}
	list := ideviceProvisionListArgs(udid)
	if !containsExact(list, "-u") || !containsExact(list, udid) || !containsExact(list, "list") {
		t.Fatalf("%v", list)
	}
	add := goiosProfileAddArgs(udid, "/tmp/a.mobileprovision")
	if !containsExact(add, "profile") || !containsExact(add, "add") || !containsExact(add, "/tmp/a.mobileprovision") {
		t.Fatalf("%v", add)
	}
}

func TestProvisionListHasUUID(t *testing.T) {
	out := "Device has 1 provisioning profiles installed:\n525db3c4-5160-4f45-98d4-7af12951b306 - iOS Team Provisioning Profile: *\n"
	if !provisionListHasUUID(out, "525db3c4-5160-4f45-98d4-7af12951b306") {
		t.Fatal("should find uuid")
	}
	if provisionListHasUUID(out, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee") {
		t.Fatal("must not match other uuid")
	}
	if provisionListHasUUID(out, "") {
		t.Fatal("empty uuid")
	}
}

func TestPingArgs(t *testing.T) {
	args := pingArgs("192.168.1.8")
	if runtime.GOOS == "windows" {
		if !containsExact(args, "-n") || !containsExact(args, "192.168.1.8") {
			t.Fatalf("windows ping args: %#v", args)
		}
	} else if !containsExact(args, "-c") || !containsExact(args, "192.168.1.8") {
		t.Fatalf("unix ping args: %#v", args)
	}
}

func containsExact(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
