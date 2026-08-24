// ipa-inspect 读取 IPA / Runner.app / .mobileprovision，判断个人（绑 UDID）还是企业 In-House。
// 输出不含设备 UDID。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"wda-farm-gateway/internal/ipasign"
)

func main() {
	ipa := flag.String("ipa", "", "signed wda.ipa")
	app := flag.String("app", "", "WebDriverAgentRunner-Runner.app")
	profile := flag.String("profile", "", ".mobileprovision")
	require := flag.String("require", "", "personal 或 enterprise；不符则 exit 2")
	field := flag.String("field", "", "只打印一个字段：mode,team,uuid,device_count,name")
	resolve := flag.Bool("resolve", false, "根据 SIGN_MODE/证书/描述文件打印最终 personal 或 enterprise")
	signMode := flag.String("sign-mode", "", "与 -resolve 一起：auto|personal|enterprise")
	identity := flag.String("identity", "", "与 -resolve 一起：CODE_SIGN_IDENTITY")
	flag.Parse()

	if *resolve {
		var p *ipasign.Profile
		if *profile != "" || *app != "" || *ipa != "" {
			src, err := loadProfile(*ipa, *app, *profile)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			parsed, err := ipasign.ParseProfile(src)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			p = &parsed
		}
		mode, err := ipasign.ResolveMode(*signMode, *identity, p)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(mode)
		return
	}

	src, err := loadProfile(*ipa, *app, *profile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	p, err := ipasign.ParseProfile(src)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	mode := p.DetectedMode()
	if *require != "" {
		want, err := ipasign.ParseMode(*require)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if want == ipasign.ModeAuto {
			fmt.Fprintln(os.Stderr, "-require 不能是 auto")
			os.Exit(1)
		}
		if err := ipasign.ValidateProfile(want, p); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}

	out := map[string]any{
		"mode":                    string(mode),
		"name":                    p.Name,
		"uuid":                    p.UUID,
		"team":                    p.Team,
		"team_name":               p.TeamName,
		"bundle_id":               p.BundleID,
		"get_task_allow":          p.GetTaskAllow,
		"provisions_all_devices":  p.ProvisionsAllDevices,
		"device_count":            p.DeviceCount,
		"has_provisioned_devices": p.HasProvisionedDevices,
		"device_bound":            p.DeviceBound(),
	}
	if *field != "" {
		v, ok := out[strings.TrimSpace(*field)]
		if !ok {
			fmt.Fprintf(os.Stderr, "未知字段 %q\n", *field)
			os.Exit(1)
		}
		fmt.Println(v)
		return
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func loadProfile(ipa, app, profile string) ([]byte, error) {
	switch {
	case profile != "":
		return decodeProvision(profile)
	case app != "":
		return decodeProvision(filepath.Join(app, "embedded.mobileprovision"))
	case ipa != "":
		return profileFromIPA(ipa)
	default:
		return nil, fmt.Errorf("usage: ipa-inspect -ipa wda.ipa | -app Runner.app | -profile file.mobileprovision")
	}
}

func profileFromIPA(ipa string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "ipa-inspect-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err := exec.Command("unzip", "-q", ipa, "-d", dir).Run(); err != nil {
		return nil, fmt.Errorf("unzip %s: %w", ipa, err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "Payload", "*.app", "embedded.mobileprovision"))
	if len(matches) == 0 {
		return nil, fmt.Errorf("IPA 里没有 embedded.mobileprovision")
	}
	return decodeProvision(matches[0])
}

func decodeProvision(path string) ([]byte, error) {
	cmd := exec.Command("security", "cms", "-D", "-i", path)
	out, err := cmd.Output()
	if err != nil {
		if raw, rerr := os.ReadFile(path); rerr == nil {
			if _, perr := ipasign.ParseProfile(raw); perr == nil {
				return raw, nil
			}
		}
		return nil, fmt.Errorf("security cms -D %s: %w", path, err)
	}
	return out, nil
}
