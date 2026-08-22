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
	if runtime.GOOS == "windows" {
		if auto != activatorGoIOS {
			t.Fatalf("windows auto want goios, got %q", auto)
		}
	} else if auto != activatorXcodebuild {
		t.Fatalf("non-windows auto want xcodebuild, got %q", auto)
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

func TestProtocolCmdMissingBinary(t *testing.T) {
	m := NewWDAManager("", "", "")
	t.Setenv("PATH", "/nonexistent")
	t.Setenv("WDA_GATEWAY_RESOURCES", "/nonexistent-resources")
	if _, _, err := m.protocolCmd("00008120-000865D90A10C01E", 8100, "", activatorGoIOS); err == nil {
		t.Fatal("expected error when ios binary is missing")
	}
	if _, _, err := m.protocolCmd("00008120-000865D90A10C01E", 8100, "", activatorTidevice); err == nil {
		t.Fatal("expected error when tidevice binary is missing")
	}
}

func TestWDABundleIDDefault(t *testing.T) {
	m := NewWDAManager("", "", "")
	if m.wdaBundleID() != defaultWDABundleID {
		t.Fatalf("default bundle id: %q", m.wdaBundleID())
	}
	m.ConfigureSigning(SigningConfig{WDABundleID: "com.example.wda.xctrunner", Activator: "goios"})
	if m.wdaBundleID() != "com.example.wda.xctrunner" {
		t.Fatalf("configured bundle id: %q", m.wdaBundleID())
	}
	if resolveActivator(m.activator) != activatorGoIOS {
		t.Fatalf("configured activator: %q", m.activator)
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
