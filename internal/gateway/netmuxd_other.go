//go:build !windows

package gateway

// netmuxdEnvName 与 Windows 实现（netmuxd.go）保持一致，便于共享代码引用。
const netmuxdEnvName = "USBMUXD_SOCKET_ADDRESS"

// syncNetmuxd 非 Windows 平台 no-op：Mac 使用系统 usbmuxd，天然提供
// ConnectionType=Network 无线条目，不需要 netmuxd。
func (g *Gateway) syncNetmuxd() {}

// StopNetmuxd 非 Windows 平台 no-op。
func (g *Gateway) StopNetmuxd() {}
