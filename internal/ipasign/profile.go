// Package ipasign 根据描述文件判断 WDA IPA 是个人（绑 UDID）还是企业 In-House（不绑设备）。
package ipasign

import (
	"fmt"
	"strings"

	"howett.net/plist"
)

// Mode 出包签名模式。
type Mode string

const (
	ModeAuto       Mode = "auto"
	ModePersonal   Mode = "personal"
	ModeEnterprise Mode = "enterprise"
	ModeUnknown    Mode = "unknown"
)

// Profile 是描述文件里和出包相关的字段。不含设备 UDID 列表。
type Profile struct {
	Name                  string
	UUID                  string
	Team                  string
	TeamName              string
	BundleID              string
	GetTaskAllow          bool
	ProvisionsAllDevices  bool
	DeviceCount           int
	HasProvisionedDevices bool
}

type rawProfile struct {
	Name                 string         `plist:"Name"`
	UUID                 string         `plist:"UUID"`
	TeamIdentifier       []string       `plist:"TeamIdentifier"`
	TeamName             string         `plist:"TeamName"`
	ProvisionsAllDevices bool           `plist:"ProvisionsAllDevices"`
	ProvisionedDevices   []string       `plist:"ProvisionedDevices"`
	Entitlements         map[string]any `plist:"Entitlements"`
}

// ParseProfile 解析 security cms -D 之后的 plist（XML 或 binary）。
func ParseProfile(data []byte) (Profile, error) {
	var raw rawProfile
	if _, err := plist.Unmarshal(data, &raw); err != nil {
		return Profile{}, fmt.Errorf("解析描述文件: %w", err)
	}
	p := Profile{
		Name:                  strings.TrimSpace(raw.Name),
		UUID:                  strings.TrimSpace(raw.UUID),
		TeamName:              strings.TrimSpace(raw.TeamName),
		ProvisionsAllDevices:  raw.ProvisionsAllDevices,
		HasProvisionedDevices: raw.ProvisionedDevices != nil,
		DeviceCount:           len(raw.ProvisionedDevices),
	}
	if len(raw.TeamIdentifier) > 0 {
		p.Team = strings.TrimSpace(raw.TeamIdentifier[0])
	}
	if raw.Entitlements != nil {
		if v, ok := raw.Entitlements["get-task-allow"].(bool); ok {
			p.GetTaskAllow = v
		}
		if id, ok := raw.Entitlements["application-identifier"].(string); ok {
			p.Team, p.BundleID = splitAppID(id, p.Team)
		}
	}
	return p, nil
}

func splitAppID(appID, team string) (string, string) {
	appID = strings.TrimSpace(appID)
	if team != "" && strings.HasPrefix(appID, team+".") {
		return team, strings.TrimPrefix(appID, team+".")
	}
	if i := strings.IndexByte(appID, '.'); i > 0 {
		return appID[:i], appID[i+1:]
	}
	return team, appID
}

// DetectedMode 只看描述文件本身：In-House 为企业，列出设备为个人/Ad Hoc。
func (p Profile) DetectedMode() Mode {
	if p.ProvisionsAllDevices {
		return ModeEnterprise
	}
	if p.HasProvisionedDevices {
		return ModePersonal
	}
	return ModeUnknown
}

// DeviceBound 描述文件是否登记了具体设备。
func (p Profile) DeviceBound() bool {
	return p.DeviceCount > 0
}

// ParseMode 识别 SIGN_MODE。空或 auto 都是 ModeAuto。
func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(ModeAuto):
		return ModeAuto, nil
	case string(ModePersonal), "individual", "adhoc", "ad-hoc", "development":
		return ModePersonal, nil
	case string(ModeEnterprise), "inhouse", "in-house":
		return ModeEnterprise, nil
	default:
		return ModeUnknown, fmt.Errorf("未知 SIGN_MODE %q（personal 或 enterprise）", s)
	}
}

// LooksLikeDistribution 企业/Ad Hoc 发行证书常用名称。
func LooksLikeDistribution(identity string) bool {
	s := strings.ToLower(identity)
	return strings.Contains(s, "iphone distribution") || strings.Contains(s, "apple distribution")
}

// ResolveMode 把显式模式、证书名和已解析描述文件收成最终出包模式。
// auto：有描述文件就跟文件走，否则个人（当前默认）。
func ResolveMode(explicit string, identity string, profile *Profile) (Mode, error) {
	want, err := ParseMode(explicit)
	if err != nil {
		return ModeUnknown, err
	}
	if want == ModeAuto {
		if profile != nil {
			if m := profile.DetectedMode(); m != ModeUnknown {
				return m, nil
			}
		}
		if LooksLikeDistribution(identity) && profile == nil {
			return ModeUnknown, fmt.Errorf("证书名像发行证书，但没有描述文件，无法区分企业 In-House 和 Ad Hoc。请设 SIGN_MODE=enterprise 或 personal")
		}
		return ModePersonal, nil
	}
	return want, nil
}

// ValidateProfile 检查描述文件是否符合目标模式。
func ValidateProfile(want Mode, p Profile) error {
	switch want {
	case ModeEnterprise:
		if !p.ProvisionsAllDevices {
			return fmt.Errorf("企业包需要 In-House 描述文件（ProvisionsAllDevices=true），当前会绑定设备，不能免 UDID 登记")
		}
		if p.DeviceCount > 0 {
			return fmt.Errorf("企业描述文件仍列出 %d 台设备，不能当作免登记包", p.DeviceCount)
		}
	case ModePersonal:
		if p.ProvisionsAllDevices && p.DeviceCount == 0 {
			return fmt.Errorf("当前描述文件是企业 In-House，与 SIGN_MODE=personal 不符")
		}
	}
	return nil
}

// SignInput 是交给 xcodebuild 的签名参数。
type SignInput struct {
	Mode             Mode
	Team             string
	Identity         string
	ProfileSpecifier string
	ProfileUUID      string
}

// ValidateSignInput 企业模式必须有发行证书和 In-House 描述文件定位方式。
func ValidateSignInput(in SignInput) error {
	if in.Mode != ModeEnterprise {
		return nil
	}
	if strings.TrimSpace(in.Identity) == "" {
		return fmt.Errorf("企业打包需要 IDENTITY（例如 iPhone Distribution: 公司名）")
	}
	if strings.TrimSpace(in.ProfileSpecifier) == "" && strings.TrimSpace(in.ProfileUUID) == "" {
		return fmt.Errorf("企业打包需要 PROFILE_SPECIFIER 或 PROFILE（In-House .mobileprovision）")
	}
	return nil
}

// XcodeSettings 返回覆盖工程签名的 xcodebuild KEY=VALUE（不含 -allowProvisioningUpdates）。
func XcodeSettings(in SignInput) []string {
	var out []string
	if t := strings.TrimSpace(in.Team); t != "" {
		out = append(out, "DEVELOPMENT_TEAM="+t)
	}
	if in.Mode != ModeEnterprise {
		return out
	}
	out = append(out, "CODE_SIGN_STYLE=Manual")
	if id := strings.TrimSpace(in.Identity); id != "" {
		out = append(out, "CODE_SIGN_IDENTITY="+id)
	}
	if spec := strings.TrimSpace(in.ProfileSpecifier); spec != "" {
		out = append(out, "PROVISIONING_PROFILE_SPECIFIER="+spec)
	}
	if uuid := strings.TrimSpace(in.ProfileUUID); uuid != "" {
		out = append(out, "PROVISIONING_PROFILE="+uuid)
	}
	return out
}

// AllowProvisioningUpdates 仅个人自动签名需要让 Xcode 更新开发描述文件。
func AllowProvisioningUpdates(mode Mode) bool {
	return mode != ModeEnterprise
}
