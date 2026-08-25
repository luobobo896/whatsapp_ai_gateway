//go:build windows

package gateway

// restartUsbmuxd 在 Windows 上拒绝重启 Apple Mobile Device Service。
// 本机实测：sc stop/start 不会挂上 ConnectionType=Network，反而让 USB
// 从 usbmux 消失，设备列表被当成掉线清空。Mac 的 killall usbmuxd 不能照搬。
func restartUsbmuxd() error {
	return errWindowsUsbmuxRestartBlocked
}
