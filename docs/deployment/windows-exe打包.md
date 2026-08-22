# Windows gateway.exe 打包

脚本：`scripts/build-windows-exe.sh`。在 macOS / Linux 上交叉编译，不需要 Windows 本机 Go。

```bash
sh scripts/build-windows-exe.sh
# 可选：GOARCH=arm64 sh scripts/build-windows-exe.sh
```

成功标志：`dist/windows-amd64/gateway.exe` 存在，脚本打印 `✅`。

`modernc.org/sqlite` 是纯 Go，构建使用 `CGO_ENABLED=0`，不依赖 MinGW。

Windows 主机上的 USB 发现 / 激活依赖另行安装的 Apple Devices + `ios.exe` / `iproxy`，不随本脚本打包（本机构没有已核验的 Windows 版 libimobiledevice 二进制）。详见产出目录 `使用说明.txt` 与 `docs/design/windows-wda-activation.md`。
