//go:build windows

package gateway

import (
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// restartUsbmuxd 重启“提供 usbmux”的 Apple 设备服务，触发 USB + 无线设备重新发现。
//
// Windows 上没有 macOS 那种 usbmuxd 守护进程；Apple Devices / iTunes 安装的 Windows 服务
// 才是 usbmux 提供者。本操作需要管理员权限（以管理员身份运行网关），服务不存在或未提权时报错。
//
// 说明：不同 Apple 版本服务名可能有别，这里逐个尝试常见名；全失败则报错并给出候选名与手动命令。
func restartUsbmuxd() error {
	candidates := []string{
		"Apple Mobile Device Service",
		"Apple Mobile Device",
		"Apple Devices Service",
		"Apple Devices",
	}
	var lastErr error
	for _, svc := range candidates {
		if err := stopStartService(svc); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("未找到 Apple 设备相关服务（请先安装 Apple Devices 或 iTunes）")
	}
	return fmt.Errorf("重启 Apple 设备服务失败（需以管理员运行网关）：%v；请手动执行：net stop \"Apple Mobile Device Service\" && net start \"Apple Mobile Device Service\"；服务名候选：%v", lastErr, candidates)
}

// stopStartService 停止并启动一个 Windows 服务；sc stop 非 0 不代表失败（可能已停止），
// 以 sc start 是否成功为准。start 前留短暂间隔避免“启动未就绪”。
func stopStartService(name string) error {
	_ = exec.Command("sc", "stop", name).Run()
	time.Sleep(800 * time.Millisecond)
	if err := exec.Command("sc", "start", name).Run(); err != nil {
		return fmt.Errorf("%s 启动失败（服务不存在或未以管理员运行）：%w", name, err)
	}
	return nil
}
