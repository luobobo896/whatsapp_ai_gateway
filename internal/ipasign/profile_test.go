package ipasign

import (
	"strings"
	"testing"
)

const personalPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Name</key>
	<string>iOS Team Provisioning Profile: *</string>
	<key>UUID</key>
	<string>11111111-2222-3333-4444-555555555555</string>
	<key>TeamIdentifier</key>
	<array>
		<string>A3JP3VUZ78</string>
	</array>
	<key>TeamName</key>
	<string>Example Personal</string>
	<key>ProvisionedDevices</key>
	<array>
		<string>aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa</string>
		<string>bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb</string>
	</array>
	<key>Entitlements</key>
	<dict>
		<key>application-identifier</key>
		<string>A3JP3VUZ78.*</string>
		<key>get-task-allow</key>
		<true/>
	</dict>
</dict>
</plist>
`

const enterprisePlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Name</key>
	<string>WDA InHouse</string>
	<key>UUID</key>
	<string>aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee</string>
	<key>TeamIdentifier</key>
	<array>
		<string>B1C2D3E4F5</string>
	</array>
	<key>TeamName</key>
	<string>Example Co</string>
	<key>ProvisionsAllDevices</key>
	<true/>
	<key>Entitlements</key>
	<dict>
		<key>application-identifier</key>
		<string>B1C2D3E4F5.com.wda.WebRunner.xctrunner</string>
		<key>get-task-allow</key>
		<false/>
	</dict>
</dict>
</plist>
`

func TestParsePersonalProfile(t *testing.T) {
	p, err := ParseProfile([]byte(personalPlist))
	if err != nil {
		t.Fatal(err)
	}
	if p.DetectedMode() != ModePersonal {
		t.Fatalf("mode=%s", p.DetectedMode())
	}
	if !p.DeviceBound() || p.DeviceCount != 2 {
		t.Fatalf("device bound count=%d", p.DeviceCount)
	}
	if p.Team != "A3JP3VUZ78" || p.BundleID != "*" || !p.GetTaskAllow {
		t.Fatalf("%+v", p)
	}
	if p.ProvisionsAllDevices {
		t.Fatal("personal must not set ProvisionsAllDevices")
	}
}

func TestParseEnterpriseProfile(t *testing.T) {
	p, err := ParseProfile([]byte(enterprisePlist))
	if err != nil {
		t.Fatal(err)
	}
	if p.DetectedMode() != ModeEnterprise {
		t.Fatalf("mode=%s", p.DetectedMode())
	}
	if p.DeviceBound() || p.HasProvisionedDevices {
		t.Fatalf("enterprise must not list devices: %+v", p)
	}
	if p.Team != "B1C2D3E4F5" || p.BundleID != "com.wda.WebRunner.xctrunner" {
		t.Fatalf("%+v", p)
	}
	if p.GetTaskAllow {
		t.Fatal("in-house typically has get-task-allow=false")
	}
}

func TestValidateProfile(t *testing.T) {
	personal, _ := ParseProfile([]byte(personalPlist))
	ent, _ := ParseProfile([]byte(enterprisePlist))
	if err := ValidateProfile(ModeEnterprise, personal); err == nil {
		t.Fatal("personal profile must not pass enterprise")
	}
	if err := ValidateProfile(ModePersonal, ent); err == nil {
		t.Fatal("enterprise profile must not pass personal")
	}
	if err := ValidateProfile(ModeEnterprise, ent); err != nil {
		t.Fatal(err)
	}
	if err := ValidateProfile(ModePersonal, personal); err != nil {
		t.Fatal(err)
	}
}

func TestValidateEnterpriseRejectsDeviceList(t *testing.T) {
	p := Profile{ProvisionsAllDevices: true, DeviceCount: 3}
	if err := ValidateProfile(ModeEnterprise, p); err == nil {
		t.Fatal("enterprise + device list must fail")
	}
}

func TestResolveMode(t *testing.T) {
	personal, _ := ParseProfile([]byte(personalPlist))
	ent, _ := ParseProfile([]byte(enterprisePlist))

	got, err := ResolveMode("", "", &personal)
	if err != nil || got != ModePersonal {
		t.Fatalf("auto personal file: %s %v", got, err)
	}
	got, err = ResolveMode("auto", "", &ent)
	if err != nil || got != ModeEnterprise {
		t.Fatalf("auto enterprise file: %s %v", got, err)
	}
	got, err = ResolveMode("", "", nil)
	if err != nil || got != ModePersonal {
		t.Fatalf("auto default: %s %v", got, err)
	}
	if _, err = ResolveMode("enterprise", "", nil); err != nil {
		t.Fatal(err)
	}
	got, err = ResolveMode("enterprise", "", nil)
	if err != nil || got != ModeEnterprise {
		t.Fatalf("explicit enterprise: %s %v", got, err)
	}
	if _, err = ResolveMode("", "iPhone Distribution: Example Co", nil); err == nil {
		t.Fatal("distribution identity without profile must not silently pick a mode")
	}
	if _, err = ResolveMode("weird", "", nil); err == nil {
		t.Fatal("unknown SIGN_MODE must fail")
	}
}

func TestXcodeSettings(t *testing.T) {
	personal := XcodeSettings(SignInput{Mode: ModePersonal, Team: "A3JP3VUZ78"})
	if len(personal) != 1 || personal[0] != "DEVELOPMENT_TEAM=A3JP3VUZ78" {
		t.Fatalf("personal settings: %#v", personal)
	}
	if !AllowProvisioningUpdates(ModePersonal) {
		t.Fatal("personal should allow provisioning updates")
	}

	ent := XcodeSettings(SignInput{
		Mode:             ModeEnterprise,
		Team:             "B1C2D3E4F5",
		Identity:         "iPhone Distribution: Example Co",
		ProfileSpecifier: "WDA InHouse",
		ProfileUUID:      "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
	})
	joined := strings.Join(ent, "\n")
	for _, want := range []string{
		"DEVELOPMENT_TEAM=B1C2D3E4F5",
		"CODE_SIGN_STYLE=Manual",
		"CODE_SIGN_IDENTITY=iPhone Distribution: Example Co",
		"PROVISIONING_PROFILE_SPECIFIER=WDA InHouse",
		"PROVISIONING_PROFILE=aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %#v", want, ent)
		}
	}
	if AllowProvisioningUpdates(ModeEnterprise) {
		t.Fatal("enterprise must not auto-update profiles")
	}
}

func TestValidateSignInput(t *testing.T) {
	if err := ValidateSignInput(SignInput{Mode: ModePersonal}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSignInput(SignInput{Mode: ModeEnterprise, Identity: "iPhone Distribution: X"}); err == nil {
		t.Fatal("enterprise without profile must fail")
	}
	if err := ValidateSignInput(SignInput{Mode: ModeEnterprise, ProfileSpecifier: "WDA InHouse"}); err == nil {
		t.Fatal("enterprise without identity must fail")
	}
	if err := ValidateSignInput(SignInput{Mode: ModeEnterprise, Identity: "iPhone Distribution: X", ProfileSpecifier: "WDA InHouse"}); err != nil {
		t.Fatal(err)
	}
}

func TestParseModeAliases(t *testing.T) {
	for _, in := range []string{"personal", "individual", "ad-hoc", "development"} {
		m, err := ParseMode(in)
		if err != nil || m != ModePersonal {
			t.Fatalf("%s -> %s %v", in, m, err)
		}
	}
	for _, in := range []string{"enterprise", "in-house", "inhouse"} {
		m, err := ParseMode(in)
		if err != nil || m != ModeEnterprise {
			t.Fatalf("%s -> %s %v", in, m, err)
		}
	}
}
