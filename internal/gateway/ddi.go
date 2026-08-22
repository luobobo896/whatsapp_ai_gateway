package gateway

// DeveloperDiskImage (DDI) 自动补齐。
//
// 背景：Xcode 16 的 CoreDevice 对 iOS ≤16 老设备（尤其 iOS 15，如 iPhone 7/7 Plus）
// 需要挂载 DeveloperDiskImage.dmg 才能安装/运行 WDA；而 DeviceSupport 目录通常只
// 在设备被 Xcode 连接过后才创建（且缺 DDI 时 xcodebuild 识别/安装不稳定）。
// 本模块在激活前自动从本机 Xcode 自带镜像复制一份到设备的 DeviceSupport 目录，
// 保证 iOS 15/16 老设备开箱即用（幂等；iOS 17+ 走 CoreDevice 原生支持，无需 DDI）。

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// xcodeDeveloperDir 返回 Xcode 开发者目录（如 /Applications/Xcode.app/Contents/Developer）。
func xcodeDeveloperDir() string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "xcode-select", "-p").Output(); err == nil {
		p := strings.TrimSpace(string(out))
		if p != "" {
			return p
		}
	}
	return "/Applications/Xcode.app/Contents/Developer"
}

// xcodeDeviceSupportDir 返回 Xcode 自带的 iOS DeviceSupport 目录。
func xcodeDeviceSupportDir() string {
	return filepath.Join(xcodeDeveloperDir(), "Platforms", "iPhoneOS.platform", "DeviceSupport")
}

// userDeviceSupportDir 返回用户（CoreDevice）的 iOS DeviceSupport 目录。
func userDeviceSupportDir() string {
	return filepath.Join(os.Getenv("HOME"), "Library", "Developer", "Xcode", "iOS DeviceSupport")
}

// ideviceInfoValue 经 ideviceinfo 查询设备属性（USB 在线且已配对时有效）。
func ideviceInfoValue(udid, key string) string {
	bin := libiDeviceBin("ideviceinfo")
	if bin == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-u", udid, "-k", key)
	cmd.Env = append(os.Environ(), bundleLibFallback()...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

var versionRe = regexp.MustCompile(`^(\d+)\.`)

// majorOf 提取版本字符串的主版本号（15.8.7 → 15）；无法解析返回 -1。
func majorOf(v string) int {
	m := versionRe.FindStringSubmatch(strings.TrimSpace(v))
	if len(m) != 2 {
		return -1
	}
	var n int
	if _, err := fmt.Sscanf(m[1], "%d", &n); err != nil {
		return -1
	}
	return n
}

// parseDDIDirVersion 从 Xcode DeviceSupport 目录名解析主版本（"15.2" → 15）。
func parseDDIDirVersion(name string) int {
	return majorOf(name)
}

// pickDDISource 从 Xcode DeviceSupport 里挑选与设备主版本匹配、且含 DDI 的镜像目录。
// 匹配策略：同主版本优先小版本最接近的（如 15.8 → 15.5 > 15.4 > 15.2 > 15.0）；
// 找不到同主版本时，iOS 15 设备用 15.5 兜底（iOS 15 的 DDI 跨小版本兼容）。
func pickDDISource(supportDir string, deviceMajor int) (string, error) {
	entries, err := os.ReadDir(supportDir)
	if err != nil {
		return "", err
	}
	type cand struct {
		dir   string
		major int
		minor int
	}
	var cands []cand
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		m := regexp.MustCompile(`^(\d+)\.(\d+)`).FindStringSubmatch(name)
		if len(m) != 3 {
			continue
		}
		var major, minor int
		if _, err := fmt.Sscanf(m[1], "%d", &major); err != nil {
			continue
		}
		if _, err := fmt.Sscanf(m[2], "%d", &minor); err != nil {
			continue
		}
		d := filepath.Join(supportDir, name)
		if _, err := os.Stat(filepath.Join(d, "DeveloperDiskImage.dmg")); err != nil {
			continue // 目录存在但无镜像，跳过
		}
		cands = append(cands, cand{dir: d, major: major, minor: minor})
	}
	if len(cands) == 0 {
		return "", fmt.Errorf("Xcode DeviceSupport 下没有可用的 DeveloperDiskImage.dmg")
	}
	// 同主版本，小版本从大到小
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].major != cands[j].major {
			return cands[i].major > cands[j].major
		}
		return cands[i].minor > cands[j].minor
	})
	for _, c := range cands {
		if c.major == deviceMajor {
			return c.dir, nil
		}
	}
	// iOS 15 设备：15.5 兜底（跨小版本兼容）
	if deviceMajor == 15 {
		for _, c := range cands {
			if c.major == 15 {
				return c.dir, nil
			}
		}
	}
	return "", fmt.Errorf("Xcode 没有 iOS %d 的 DeveloperDiskImage（设备目录：%v）", deviceMajor, cands)
}

// EnsureDeviceSupportDDI 确保 UDID 设备在 CoreDevice 的 DeviceSupport 目录里有
// DeveloperDiskImage.dmg + .signature（iOS ≤16 老设备激活 WDA 必需）。
// 设备需 USB 在线以便查询型号/版本；查询失败或 iOS 17+ 时静默跳过（不阻塞激活）。
// 幂等：目录已含 DDI 则立即返回。
func EnsureDeviceSupportDDI(udid string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	pt := ideviceInfoValue(udid, "ProductType")     // 如 iPhone9,2
	ver := ideviceInfoValue(udid, "ProductVersion") // 如 15.8.7
	build := ideviceInfoValue(udid, "BuildVersion") // 如 19H411
	if pt == "" || ver == "" {
		slog.Warn("EnsureDeviceSupportDDI: 无法读取设备型号/版本（可能未插 USB 或未配对），跳过", "udid", shortOf(udid))
		return nil
	}
	major := majorOf(ver)
	if major >= 17 {
		// iOS 17+ 由 CoreDevice 原生支持，无需挂载 DDI。
		return nil
	}

	dir := filepath.Join(userDeviceSupportDir(), fmt.Sprintf("%s %s (%s)", pt, ver, build))
	if _, err := os.Stat(filepath.Join(dir, "DeveloperDiskImage.dmg")); err == nil {
		return nil // 已就绪
	}
	src, err := pickDDISource(xcodeDeviceSupportDir(), major)
	if err != nil {
		slog.Warn("EnsureDeviceSupportDDI: 找不到可用镜像", "udid", shortOf(udid), "ver", ver, "err", err)
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, f := range []string{"DeveloperDiskImage.dmg", "DeveloperDiskImage.dmg.signature"} {
		data, err := os.ReadFile(filepath.Join(src, f))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, f), data, 0o644); err != nil {
			return err
		}
	}
	slog.Info("EnsureDeviceSupportDDI: 已补齐 DeveloperDiskImage", "udid", shortOf(udid), "ver", ver, "build", build, "src", filepath.Base(src))
	return nil
}

// shortOf 截断 UDID 用于日志（前 8 位），防止超长串刷屏。
func shortOf(udid string) string {
	if len(udid) > 8 {
		return udid[:8]
	}
	return udid
}
