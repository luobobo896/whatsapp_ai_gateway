# Windows gateway.exe + 桌面壳打包

脚本：`scripts/build-windows-exe.sh`。在 macOS / Linux 上交叉编译，不需要 Windows 本机 Go。

```bash
sh scripts/build-windows-exe.sh
# 可选：GOARCH=arm64 sh scripts/build-windows-exe.sh
# 可选：SKIP_DESKTOP=1 只编 gateway/wda-probe
```

成功标志：

- `dist/windows-amd64/gateway.exe` 存在
- `dist/windows-amd64/WDAFarmGateway.exe` 存在（桌面壳）
- 脚本打印 `✅`

`modernc.org/sqlite` 是纯 Go，构建使用 `CGO_ENABLED=0`，不依赖 MinGW。  
桌面壳依赖 `github.com/jchv/go-webview2` + `github.com/energye/systray`（同样无 CGO）。

Windows 主机上的 USB 发现 / 激活依赖另行安装的 Apple Devices + `ios.exe` / `tidevice` / `iproxy`，不随本脚本打包。  
桌面壳还需要本机 **WebView2 Runtime**（见 [windows-desktop-shell.md](./windows-desktop-shell.md)）。

当晚回家按 [windows-night-runbook.md](./windows-night-runbook.md) 验收。设计说明见 [windows-wda-activation.md](../design/windows-wda-activation.md)。

## 后续 MSI

zip 够用后再上 Inno Setup / WiX：把 `dist/windows-amd64/*` 装到 `%LocalAppData%\WDAFarmGateway\`，创建开始菜单快捷方式指向 `WDAFarmGateway.exe`，可选捆绑 [WebView2 Evergreen Bootstrapper](https://developer.microsoft.com/microsoft-edge/webview2/)。Authenticode 签名另备证书。
