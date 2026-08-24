package gateway

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"wda-farm-gateway/internal/ipasign"
)

func ideviceProvisionInstallArgs(udid, file string) []string {
	return []string{"-u", udid, "install", file}
}

func ideviceProvisionListArgs(udid string) []string {
	return []string{"-u", udid, "list"}
}

func goiosProfileAddArgs(udid, file string) []string {
	return []string{"--udid=" + udid, "profile", "add", file}
}

func provisionListHasUUID(out, uuid string) bool {
	uuid = strings.TrimSpace(uuid)
	return uuid != "" && strings.Contains(out, uuid)
}

func teamNameFromIPA(ipa string) string {
	raw, err := ipasign.ExtractEmbeddedProvision(ipa)
	if err != nil {
		return ""
	}
	p, err := ipasign.ParseProvisionBytes(raw)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(p.TeamName)
}

// installSigningProfile 把 IPA 内嵌描述文件装到手机。个人和企业同一条路，不区分账号类型。
// 装不上只打日志，不挡后续 IPA 安装。
func (m *WDAManager) installSigningProfile(udid, kind, iosVersion, ipa string) {
	if strings.TrimSpace(ipa) == "" || strings.TrimSpace(udid) == "" {
		return
	}
	raw, err := ipasign.ExtractEmbeddedProvision(ipa)
	if err != nil {
		slog.Warn("extract ipa provision failed", "udid", shortOf(udid), "error", err)
		return
	}
	p, perr := ipasign.ParseProvisionBytes(raw)
	if perr != nil {
		slog.Warn("parse ipa provision failed", "udid", shortOf(udid), "error", perr)
	} else if p.UUID != "" {
		if bin := lookTool("ideviceprovision", "ideviceprovision.exe"); bin != "" {
			out, lerr := runTool(bin, ideviceProvisionListArgs(udid), 8*time.Second)
			if lerr == nil && provisionListHasUUID(out, p.UUID) {
				slog.Info("signing profile already on device", "udid", shortOf(udid), "mode", p.DetectedMode())
				return
			}
		}
	}

	tmp, err := os.CreateTemp("", "wda-*.mobileprovision")
	if err != nil {
		return
	}
	path := tmp.Name()
	_, werr := tmp.Write(raw)
	_ = tmp.Close()
	defer os.Remove(path)
	if werr != nil {
		return
	}

	if err := m.pushProvisionFile(udid, kind, iosVersion, path); err != nil {
		slog.Warn("install signing profile failed", "udid", shortOf(udid), "error", err)
		return
	}
	mode := ipasign.ModeUnknown
	if perr == nil {
		mode = p.DetectedMode()
	}
	slog.Info("installed signing profile from IPA", "udid", shortOf(udid), "mode", mode)
}

func (m *WDAManager) pushProvisionFile(udid, kind, iosVersion, file string) error {
	if bin := lookTool("ideviceprovision", "ideviceprovision.exe"); bin != "" {
		if _, err := runTool(bin, ideviceProvisionInstallArgs(udid, file), 20*time.Second); err == nil {
			return nil
		} else {
			slog.Warn("ideviceprovision install failed, trying go-ios", "udid", shortOf(udid), "error", err)
		}
	}
	if kind == activatorGoIOS || lookTool("ios", "ios.exe") != "" {
		bin := lookTool("ios", "ios.exe")
		if bin == "" {
			return fmt.Errorf("未找到 ideviceprovision / ios，无法自动安装描述文件")
		}
		args := goiosProfileAddArgs(udid, file)
		if needsRemoteXPCTunnel(iosVersion) {
			args = withGoIOSTunnelPort(args)
		}
		if _, err := runTool(bin, args, 20*time.Second); err != nil {
			return err
		}
		return nil
	}
	return fmt.Errorf("未找到 ideviceprovision / ios，无法自动安装描述文件")
}
