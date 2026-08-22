# Windows gateway.exe 打包

脚本：`scripts/build-windows-exe.sh`。在 macOS / Linux 上交叉编译，不需要 Windows 本机 Go。

```bash
sh scripts/build-windows-exe.sh
# 可选：GOARCH=arm64 sh scripts/build-windows-exe.sh
```

成功标志：`dist/windows-amd64/gateway.exe` 存在，脚本打印 `✅`。

`modernc.org/sqlite` 是纯 Go，构建使用 `CGO_ENABLED=0`，不依赖 MinGW。

Windows 主机上的 USB 发现 / 激活依赖另行安装的 Apple Devices + `ios.exe` / `tidevice` / `iproxy`，不随本脚本打包（本机构没有已核验的 Windows 版 libimobiledevice 二进制）。

当晚回家按 [windows-night-runbook.md](./windows-night-runbook.md) 验收。设计说明见 [windows-wda-activation.md](../design/windows-wda-activation.md)。
