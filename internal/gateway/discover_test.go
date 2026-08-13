package gateway

import (
	"net"
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
