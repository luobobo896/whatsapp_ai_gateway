# USB / Network 激活互斥

日期：2026-08-25

USB 与 Network 是两条独立通道，互斥、不混用、不自动兜底。

| 动作 | USB | Network |
|---|---|---|
| 激活 | 必须 USB 在线；`ios runwda` / xcodebuild / tidevice | 必须已 USB 首次授权；`wifi-runwda -require-network`（不写 lockdown） |
| 探活 / 发送 | 只走 USB iproxy | 只走 iproxy `-n` 或已记录 Wi-Fi IP |
| 看护重激活 | 只在 USB 仍在时 | 只在 usbmux Network 或 Wi-Fi 可达时 |
| 停止 | 杀主机进程 + 拆本通道隧道 + 不再用残留 `/status` 救活 | 同左 |

明确拒绝：iOS 17+ 的 Network 激活改走 go-ios USB 隧道、tidevice/xcodebuild 当 Network、USB 激活改走 Wi-Fi。

```bash
go test ./internal/gateway -count=1 -timeout 180s \
  -run 'TestProtocolCmd|TestChannelReachableForVia|TestUserStopped|TestWdaBaseURLFor|TestWdaProbeVia|TestConnTypeOfDevice|TestParseActivateVia'
```
