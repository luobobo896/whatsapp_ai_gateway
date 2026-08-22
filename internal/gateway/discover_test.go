package gateway

import (
	"net"
	"strings"
	"testing"
)

func TestIsPrivateIPv4(t *testing.T) {
	cases := map[string]bool{
		"10.0.0.1":       true,
		"172.16.0.1":     true,
		"172.31.255.255": true,
		"192.168.1.1":    true,
		"172.15.0.1":     false,
		"172.32.0.1":     false,
		"100.64.0.1":     false,
		"198.18.0.1":     false,
		"169.254.1.1":    false,
		"8.8.8.8":        false,
	}
	for s, want := range cases {
		if got := isPrivateIPv4(net.ParseIP(s).To4()); got != want {
			t.Errorf("isPrivateIPv4(%s) = %v, want %v", s, got, want)
		}
	}
}

func TestIOSMajor(t *testing.T) {
	cases := map[string]int{"15.8.8": 15, "16.4.1": 16, "18.4": 18, "": 0, "abc": 0}
	for in, want := range cases {
		if got := iosMajor(in); got != want {
			t.Errorf("iosMajor(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestValidSerial(t *testing.T) {
	cases := map[string]bool{
		"C38SG3S0HG00": true,                        // 经典 12 位
		"QP7X22L3JH":   true,                        // 旧款 11 位
		"000081100001A1B2C3D4E5F6": true,            // 新式 24 位
		"short1": false,                             // 过短
		"": false,
		"ERROR: Unable to connect": false,           // ideviceinfo 错误文本
		"含中文AAA111": false, // 非字母数字
	}
	for s, want := range cases {
		if got := validSerial(s); got != want {
			t.Errorf("validSerial(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestLooksLikeUDID(t *testing.T) {
	cases := map[string]bool{
		"4886579a97a96bad83b527862bab409b5a07c741": true,  // 老机型 40 位 hex（usbmux 原文）
		"00008120-000865d90a10c01e":                true,  // iPhone XS 及以后 8-16 带连字符（usbmux 原文）
		"00008120000865d90a10c01e":                 false, // 无连字符 24 位：iproxy/ideviceinfo 不认，非标准格式
		"a46bad8f-21e5-54c0-9960-e4aac7a4aa04":     false, // devicectl 的 CoreDevice UUID，不是 UDID
		"":                                         false,
		"001000001":                                false, // USB Hub serial
		"not-a-udid":                               false,
	}
	for s, want := range cases {
		if got := looksLikeUDID(s); got != want {
			t.Errorf("looksLikeUDID(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestLanFingerprintStableFormat(t *testing.T) {
	got := lanFingerprint()
	// 无网卡时允许空；有值时必须是排序后的 /24 列表。
	if got == "" {
		return
	}
	parts := strings.Split(got, ",")
	for i, p := range parts {
		if !strings.HasSuffix(p, "/24") {
			t.Fatalf("part %d = %q, want /24 suffix", i, p)
		}
		if i > 0 && parts[i-1] > p {
			t.Fatalf("not sorted: %q", got)
		}
	}
}
