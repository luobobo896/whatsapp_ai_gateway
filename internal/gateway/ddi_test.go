package gateway

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMajorOf(t *testing.T) {
	cases := map[string]int{
		"15.8.7": 15, "16.5": 16, "17.0.1": 17, "26.6": 26,
		"": -1, "abc": -1, "19H411": -1,
	}
	for in, want := range cases {
		if got := majorOf(in); got != want {
			t.Errorf("majorOf(%q)=%d want %d", in, got, want)
		}
	}
}

func TestParseDDIDirVersion(t *testing.T) {
	if got := parseDDIDirVersion("15.2"); got != 15 {
		t.Errorf("parseDDIDirVersion(15.2)=%d want 15", got)
	}
	if got := parseDDIDirVersion("16.4"); got != 16 {
		t.Errorf("parseDDIDirVersion(16.4)=%d want 16", got)
	}
}

// TestPickDDISourceRealXcode 在真实 Xcode DeviceSupport 上验证挑选逻辑：
// iOS 15 设备应命中 15.x 镜像（本机 Xcode 16.4 自带 15.0/15.2/15.4/15.5）。
func TestPickDDISourceRealXcode(t *testing.T) {
	dir := xcodeDeviceSupportDir()
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("Xcode DeviceSupport 不存在: %v", err)
	}
	src, err := pickDDISource(dir, 15)
	if err != nil {
		t.Fatalf("pickDDISource(15) failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(src, "DeveloperDiskImage.dmg")); err != nil {
		t.Fatalf("所选源缺少 dmg: %v", err)
	}
	t.Logf("iOS15 命中镜像源: %s", filepath.Base(src))
	if _, err := pickDDISource(dir, 16); err != nil {
		t.Errorf("pickDDISource(16) failed: %v", err)
	}
}

// TestEnsureDeviceSupportDDIReal 真实设备（USB 在线时）验证 DDI 补齐幂等；
// 并验证"缺 DDI 时自动补回"：把已就绪目录的 dmg 临时移走，调用后应恢复。
func TestEnsureDeviceSupportDDIReal(t *testing.T) {
	udid := "5060c403afdee4c15a0edeab69dba0524e2ce592"
	if err := EnsureDeviceSupportDDI(udid); err != nil {
		t.Fatalf("EnsureDeviceSupportDDI failed: %v", err)
	}
	// 定位目标目录：从 ideviceinfo 查型号/版本/构建
	pt := ideviceInfoValue(udid, "ProductType")
	ver := ideviceInfoValue(udid, "ProductVersion")
	build := ideviceInfoValue(udid, "BuildVersion")
	if pt == "" || ver == "" || build == "" {
		t.Skip("设备不在线或未配对，跳过集成验证")
	}
	dir := filepath.Join(userDeviceSupportDir(), pt+" "+ver+" ("+build+")")
	dmg := filepath.Join(dir, "DeveloperDiskImage.dmg")
	if _, err := os.Stat(dmg); err != nil {
		t.Fatalf("DDI 未就绪: %v", err)
	}
	// 移走 dmg，验证自动补回
	tmp := dmg + ".bak-test"
	if err := os.Rename(dmg, tmp); err != nil {
		t.Fatalf("rename dmg away failed: %v", err)
	}
	defer os.Rename(tmp, dmg)
	if err := EnsureDeviceSupportDDI(udid); err != nil {
		t.Fatalf("重新补齐失败: %v", err)
	}
	if _, err := os.Stat(dmg); err != nil {
		t.Fatalf("补齐后 dmg 仍不存在: %v", err)
	}
	t.Logf("DDI 自动补齐验证通过: %s", filepath.Base(dmg))
}
