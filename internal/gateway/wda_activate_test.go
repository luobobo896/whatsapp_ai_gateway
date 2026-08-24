package gateway

import (
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

func TestEnableWifiLockdownMissingBinaryIsNoop(t *testing.T) {
	t.Setenv("PATH", "/nonexistent")
	t.Setenv("WDA_GATEWAY_RESOURCES", "/nonexistent-resources")
	enableWifiLockdown("00008120-000865D90A10C01E")
}

func TestProtocolCmdMissingBinary(t *testing.T) {
	m := NewWDAManager("", "", "")
	t.Setenv("PATH", "/nonexistent")
	t.Setenv("WDA_GATEWAY_RESOURCES", "/nonexistent-resources")
	if _, _, err := m.protocolCmd("00008120-000865D90A10C01E", 8100, "", activatorGoIOS, "", "18.6"); err == nil {
		t.Fatal("expected error when ios binary is missing")
	}
	if _, _, err := m.protocolCmd("00008120-000865D90A10C01E", 8100, "", activatorTidevice, "", "15.8.7"); err == nil {
		t.Fatal("expected error when tidevice binary is missing")
	}
}

func TestWifiRunwdaInvocationMissingBinary(t *testing.T) {
	t.Setenv("PATH", "/nonexistent")
	t.Setenv("WDA_GATEWAY_RESOURCES", "/nonexistent-resources")
	if _, _, ok := wifiRunwdaInvocation("00008120-000865D90A10C01E", "127.0.0.1", 8100, "", defaultWDABundleID); ok {
		t.Fatal("missing wifi-runwda must not be selected")
	}
}

func TestWifiRunwdaInvocationBuildsArgs(t *testing.T) {
	dir := plantActivateTools(t, true, true)
	t.Setenv("PATH", dir)
	t.Setenv("WDA_GATEWAY_RESOURCES", filepath.Join(dir, "no-resources"))
	udid := "4886579a97a96bad83b527862bab409b5a07c741"
	bin, args, ok := wifiRunwdaInvocation(udid, "192.168.10.237", 8100, "", defaultWDABundleID)
	if !ok {
		t.Fatal("expected wifi-runwda when both binaries resolve")
	}
	if bin == "" {
		t.Fatal("empty wrapper path")
	}
	for _, want := range []string{"-udid", udid, "-port", "8100", "-bundle", defaultWDABundleID, "-ios", "-ip", "192.168.10.237", "-wait-network", "8s"} {
		if !containsExact(args, want) {
			t.Fatalf("missing %q in %#v", want, args)
		}
	}
}

func TestProtocolCmdIOS15UsesWifiRunwda(t *testing.T) {
	dir := plantActivateTools(t, true, true)
	t.Setenv("PATH", dir)
	t.Setenv("WDA_GATEWAY_RESOURCES", filepath.Join(dir, "no-resources"))
	m := NewWDAManager("", "", "")
	udid := "4886579a97a96bad83b527862bab409b5a07c741"
	bin, args, err := m.protocolCmd(udid, 8100, udid, activatorGoIOS, "192.168.10.237", "15.8.7")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(bin) != "wifi-runwda" {
		t.Fatalf("bin=%q want wifi-runwda so XCTest can survive USB unplug", bin)
	}
	if !containsExact(args, "-wait-network") || !containsExact(args, "8s") {
		t.Fatalf("must briefly wait for usbmux Network then USB: %#v", args)
	}
}

func TestProtocolCmdIOS15RequiresWifiRunwda(t *testing.T) {
	dir := plantActivateTools(t, false, true)
	t.Setenv("PATH", dir)
	t.Setenv("WDA_GATEWAY_RESOURCES", filepath.Join(dir, "no-resources"))
	m := NewWDAManager("", "", "")
	_, _, err := m.protocolCmd("5060c403afdee4c15a0edeab69dba0524e2ce592", 8100, "", activatorGoIOS, "", "15.8.7")
	if err == nil {
		t.Fatal("iOS 15 without wifi-runwda must not silently USB-activate")
	}
	if !strings.Contains(err.Error(), "wifi-runwda") {
		t.Fatalf("error=%v", err)
	}
}

func TestProtocolCmdGoIOSWithoutWrapper(t *testing.T) {
	dir := plantActivateTools(t, false, true)
	t.Setenv("PATH", dir)
	t.Setenv("WDA_GATEWAY_RESOURCES", filepath.Join(dir, "no-resources"))
	m := NewWDAManager("", "", "")
	udid := "00008120-000865D90A10C01E"
	bin, args, err := m.protocolCmd(udid, 8100, "", activatorGoIOS, "", "18.6")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(bin) != "ios" {
		t.Fatalf("bin=%q want ios", bin)
	}
	if !containsExact(args, "runwda") || !containsExact(args, "--tunnel-info-port=28100") {
		t.Fatalf("iOS 17+ without wrapper must still use tunnel runwda: %#v", args)
	}
}

func plantActivateTools(t *testing.T, wrap, ios bool) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nexit 0\n"
	if wrap {
		if err := os.WriteFile(filepath.Join(dir, "wifi-runwda"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if ios {
		if err := os.WriteFile(filepath.Join(dir, "ios"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestProtocolCmdIOS17SkipsWifiRunwda(t *testing.T) {
	dir := plantActivateTools(t, true, true)
	t.Setenv("PATH", dir)
	t.Setenv("WDA_GATEWAY_RESOURCES", filepath.Join(dir, "no-resources"))
	m := NewWDAManager("", "", "")
	udid := "00008120-000865D90A10C01E"
	bin, args, err := m.protocolCmd(udid, 8100, udid, activatorGoIOS, "192.168.10.237", "18.6")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(bin) == "wifi-runwda" {
		t.Fatal("iOS 17+ must not use wifi-runwda")
	}
	if filepath.Base(bin) != "ios" {
		t.Fatalf("bin=%q want ios", bin)
	}
	if !containsExact(args, "runwda") || !containsExact(args, "--tunnel-info-port=28100") {
		t.Fatalf("runwda tunnel args: %#v", args)
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
