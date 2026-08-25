# 随仓工具

这些文件以前被 `.gitignore` 丢掉，别人 `git pull` 后无法激活设备。现在入库。

| 文件 | 用途 | 平台 |
|---|---|---|
| `ios` | go-ios 1.3.2，`runwda` / `tunnel start` / `install` | macOS universal (arm64 + x86_64) |
| `ios.exe` | 同上 | Windows amd64 |
| `wifi-runwda` / `wifi-runwda.exe` | 必须走 usbmux Network 拉起 WDA（iOS 15–16）；网关带 `-require-network`，没有 Network 则失败、不回退 USB。Mac/Windows 同一套规则 | Mac arm64 / Windows amd64 |
| `wifi-lockdown` / `wifi-lockdown.exe` | 仅 USB 下写 `EnableWifiConnections` / `EnableWifiDebugging`（`-status` 只读）。Mac/Windows 同一套授权规则；Windows 额外按 iTunes Buddy 重绑。密码不进电脑 | Mac arm64 / Windows amd64 |
| `netmuxd.exe` + `netmuxd-LICENSE.txt` | Windows 专用：Apple usbmuxd 重实现（LGPL-2.1）。mDNS 发现无线设备 + heartbeat 保活，shim 模式把 USB 转发给 AMDS，让无线 WDA 拔掉 USB 后不断开 | Windows amd64 |
| `wda.ipa` | 已签名的 WebDriverAgent Runner，激活时自动安装 | 所有主机。个人包描述文件须含该机 UDID；企业 In-House 包不绑设备 |
| `easytier/easytier-core`、`easytier-cli` | 可选组网；打 Mac DMG 需要 | macOS arm64 |

源码运行（仓库根目录）：

```bash
go build -o gateway ./cmd/gateway
./gateway -state . -listen 0.0.0.0:8300
```

网关会在 `tools/` 里找 `ios` 和 `wda.ipa`，不必再单独配 PATH。

Windows 还要本机安装 Apple Devices / iTunes（提供 usbmux）。`ios.exe` 已随仓；网关自动拉起 `netmuxd.exe`（Network 无线保活）；`iproxy` 仍需自行准备或用包内 `bin/`。

`netmuxd` 只在 Windows 生效：源码中 `netmuxd.go`/`netmuxd_test.go` 带 `//go:build windows`，
非 Windows 平台由 `netmuxd_other.go` 提供同签名 no-op 桩，Mac 构建不受影响。

不要提交 `data/`、`gateway.db`、云凭证。
