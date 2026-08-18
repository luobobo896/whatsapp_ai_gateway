package gateway

import "testing"

// decideIPAssignments 覆盖：隧道自报优先、UUID 强匹配、唯一性兜底、
// 已占用 IP 不抢占、多候选不猜、非私网自报拒绝。
func TestDecideIPAssignments(t *testing.T) {
	owner := map[string]string{"192.168.1.10": "dev-old"}
	found := []FoundWDA{
		{IP: "192.168.1.10", UUID: "u-old"}, // 已被 dev-old 占用
		{IP: "192.168.1.11", UUID: "u-a"},
		{IP: "192.168.1.12", UUID: "u-b"},
	}

	t.Run("隧道自报优先且防抢占", func(t *testing.T) {
		pending := []pendingIPDev{
			{udid: "d1", selfIP: "192.168.1.50"},          // 私网无主 → 采纳
			{udid: "d2", selfIP: "192.168.1.10"},          // 已被占用 → 不采纳
			{udid: "d3", selfIP: "169.254.1.5"},           // 非私网（链路本地）→ 拒绝
			{udid: "d4", vendorUUID: "u-a", selfIP: ""},   // 无自报，走 UUID 匹配
		}
		got := decideIPAssignments(pending, found, owner)
		if got["d1"] != "192.168.1.50" {
			t.Errorf("d1 应采纳自报 IP，got %v", got)
		}
		if _, ok := got["d2"]; ok {
			t.Errorf("d2 自报 IP 已被占用，不应分配: %v", got)
		}
		if _, ok := got["d3"]; ok {
			t.Errorf("d3 自报非私网 IP，不应分配: %v", got)
		}
		if got["d4"] != "192.168.1.11" {
			t.Errorf("d4 应按 UUID 匹配到 u-a，got %v", got)
		}
	})

	t.Run("多无主候选时无 UUID 不猜", func(t *testing.T) {
		pending := []pendingIPDev{{udid: "d1"}} // 无自报、无 UUID
		got := decideIPAssignments(pending, found, owner)
		if _, ok := got["d1"]; ok {
			t.Errorf("存在 2 个无主候选（.11/.12），唯一性规则不应触发，got %v", got)
		}
	})

	t.Run("恰好一对一走唯一性", func(t *testing.T) {
		single := []FoundWDA{{IP: "192.168.1.11", UUID: "u-a"}}
		got := decideIPAssignments([]pendingIPDev{{udid: "d1"}}, single, owner)
		if got["d1"] != "192.168.1.11" {
			t.Errorf("1 待分配 + 1 无主候选应分配，got %v", got)
		}
	})

	t.Run("UUID 匹配跳过已占用候选", func(t *testing.T) {
		pending := []pendingIPDev{{udid: "d1", vendorUUID: "u-old"}} // u-old 的 IP 已被 dev-old 占
		got := decideIPAssignments(pending, found, owner)
		if _, ok := got["d1"]; ok {
			t.Errorf("UUID 命中但候选已被占用，不应分配: %v", got)
		}
	})

	t.Run("多台设备同轮各自认领不冲突", func(t *testing.T) {
		pending := []pendingIPDev{
			{udid: "d1", vendorUUID: "u-a"},
			{udid: "d2", vendorUUID: "u-b"},
		}
		got := decideIPAssignments(pending, found, owner)
		if got["d1"] != "192.168.1.11" || got["d2"] != "192.168.1.12" {
			t.Errorf("两台应各自按 UUID 认领不同 IP，got %v", got)
		}
	})
}

// physicalSubnets 应是 privateSubnets 的子集（只含 en* 网卡网段）。
func TestPhysicalSubnetsSubset(t *testing.T) {
	full := map[string]bool{}
	for _, s := range privateSubnets() {
		full[s] = true
	}
	for _, s := range physicalSubnets() {
		if !full[s] {
			t.Errorf("物理网段 %s 不在全量网段中", s)
		}
	}
}
