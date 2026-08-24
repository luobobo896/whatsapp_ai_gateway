package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectResolveAndRequire(t *testing.T) {
	dir := t.TempDir()
	personal := filepath.Join(dir, "personal.plist")
	enterprise := filepath.Join(dir, "enterprise.plist")
	if err := os.WriteFile(personal, []byte(personalXML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(enterprise, []byte(enterpriseXML), 0o644); err != nil {
		t.Fatal(err)
	}

	out := runInspect(t, "-resolve", "-sign-mode", "auto", "-profile", personal)
	if strings.TrimSpace(out) != "personal" {
		t.Fatalf("auto personal file: %q", out)
	}
	out = runInspect(t, "-resolve", "-sign-mode", "auto", "-profile", enterprise)
	if strings.TrimSpace(out) != "enterprise" {
		t.Fatalf("auto enterprise file: %q", out)
	}
	out = runInspect(t, "-field", "device_count", "-profile", personal)
	if strings.TrimSpace(out) != "2" {
		t.Fatalf("device_count=%q", out)
	}
	out = runInspect(t, "-field", "device_count", "-profile", enterprise)
	if strings.TrimSpace(out) != "0" {
		t.Fatalf("enterprise device_count=%q", out)
	}
	if err := inspectErr("-profile", personal, "-require", "enterprise"); err == nil {
		t.Fatal("personal profile must fail -require enterprise")
	}
	if err := inspectErr("-profile", enterprise, "-require", "enterprise"); err != nil {
		t.Fatal(err)
	}
}

func runInspect(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("go", append([]string{"run", "."}, args...)...)
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ipa-inspect %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func inspectErr(args ...string) error {
	cmd := exec.Command("go", append([]string{"run", "."}, args...)...)
	cmd.Dir = "."
	_, err := cmd.CombinedOutput()
	return err
}

const personalXML = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Name</key><string>Dev</string>
	<key>UUID</key><string>11111111-2222-3333-4444-555555555555</string>
	<key>TeamIdentifier</key><array><string>A3JP3VUZ78</string></array>
	<key>ProvisionedDevices</key>
	<array>
		<string>aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa</string>
		<string>bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb</string>
	</array>
	<key>Entitlements</key>
	<dict>
		<key>application-identifier</key><string>A3JP3VUZ78.*</string>
		<key>get-task-allow</key><true/>
	</dict>
</dict>
</plist>`

const enterpriseXML = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Name</key><string>InHouse</string>
	<key>UUID</key><string>aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee</string>
	<key>TeamIdentifier</key><array><string>B1C2D3E4F5</string></array>
	<key>ProvisionsAllDevices</key><true/>
	<key>Entitlements</key>
	<dict>
		<key>application-identifier</key><string>B1C2D3E4F5.com.wda.WebRunner.xctrunner</string>
		<key>get-task-allow</key><false/>
	</dict>
</dict>
</plist>`
