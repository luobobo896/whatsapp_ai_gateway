//go:build !windows

package gateway

import (
	"errors"
	"os"
	"os/exec"
)

// restartUsbmuxd 重启系统 usbmuxd（macOS/Linux；Apple 的 launchd 守护进程会自动重新拉起）。
// 权限优先级：当前进程是 root -> sudo -n（sudoers 授权）-> osascript 弹管理员授权。
func restartUsbmuxd() error {
	if os.Geteuid() == 0 {
		return exec.Command("/usr/bin/killall", "usbmuxd").Run()
	}
	if exec.Command("/usr/bin/sudo", "-n", "/usr/bin/killall", "usbmuxd").Run() == nil {
		return nil
	}
	script := `do shell script "/usr/bin/killall usbmuxd" with administrator privileges`
	if exec.Command("/usr/bin/osascript", "-e", script).Run() == nil {
		return nil
	}
	return errors.New("重启 usbmuxd 需要 root 权限：请先执行 scripts/setup-usbmux-sudo.sh 授予免密权限，或在弹出的系统授权框输入管理员密码")
}
