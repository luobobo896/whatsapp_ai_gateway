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

## 拔线后保活与清理边界

- `unplugSafeFor(udid, ios, …)`：iOS ≤16 依赖 usbmux `ConnectionType=Network` 条目（`netSet`），iOS 17+ 依赖 go-ios 隧道（`tunnelSet`）。`/api/usbmux-net` 的 `unplug_safe` 由此得出。
- `wifi_debug=true`（已无线授权）的设备 **掉线只隐藏、不物理删除**（`pruneOfflineDevices` 显式跳过），避免 iOS 空闲关闭无线会话后丢失 `activate_via/wifi_debug/ip` 配置、手机重新广播时还得插 USB 重新授权。
- 边界：设备失去 USB 且 WDA 探活失败（如 `connection refused`）时，只要 usbmux 仍有 Network 条目，看护的 `channelReachableForVia(network)` 仍判可达 → 会持续 `WDA down, reactivating`，出现「永远都有 `wifi-runwda+ios runwda`」。这是**由设计保留配置**（非物理删除）导致的重激活循环。
- 处置：点管理页「停止」→ `Stop` API 置 `auto_reactivate=false`，立即停止重激活并拆掉残留 runwda/隧道；健康记录变为 `stopped`。设备重新插回并做一次网络激活即可恢复。若确要彻底移除，用 `DELETE /api/devices/<udid>`（会丢掉 `wifi_debug/activate_via` 配置）。
