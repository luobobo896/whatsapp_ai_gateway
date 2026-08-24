# 2026-08-24 激活后拔 USB 走 Wi-Fi

- 日期：2026-08-24
- 目的：Automation Running 之后拔 USB，WDA 继续走手机 Wi-Fi `:8100`

## 规则

iOS 15–16：

1. `wifi-lockdown` 打开 `EnableWifiConnections`
2. `wifi-runwda -wait-network 45s` 等到 usbmux `ConnectionType=Network`
3. 全部 lockdown / testmanagerd Connect 走 Network
4. 最多等 8s；没有 Network 就 **USB 回退**（插着能激活，拔线仍会拆 XCTest）
5. 激活成功标准是机上 `/status` ready，不是主机进程还在等

iOS 17+ 仍走 go-ios 用户态隧道，不劫持 usbmux。

## 验证

```bash
go test ./internal/gateway ./internal/wda ./cmd/wifi-runwda -count=1
```

覆盖：

- `TestProtocolCmdIOS15UsesWifiRunwda`
- `TestProtocolCmdIOS15RequiresWifiRunwda`
- `TestProtocolCmdIOS17SkipsWifiRunwda`
- `TestRequireMuxNetwork` / `TestWaitPreferNetworkUSBFallback`（USB 不得当作可激活）

真机：

1. `ios list --details` 必须先出现该机 `ConnectionType=Network`（可与 USB 并存）
2. 点激活，日志为 `activate via wifi-runwda` 且 `via=Network`
3. 顶栏 Automation Running 后拔 USB
4. `curl http://<ios.ip>:8100/status` 仍 200 / ready
