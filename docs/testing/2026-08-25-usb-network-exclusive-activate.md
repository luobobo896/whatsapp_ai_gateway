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

## 管理页按钮：Wi-Fi 设备不显示「USB 激活」

设备卡片（`static/index.html` 的 `deviceCard`）按通道/连接类型决定激活入口，避免误点报错：

- `activate_via=network`（Wi-Fi/Network 类型）**或当前未接 USB**（`!d.usb`）的设备：**不显示「USB 激活」**，主按钮为「Network 激活」；未完成首次无线授权时改为「首次授权」+ 禁用的「Network 激活」。
- 健康（`/status` 已通）的 network 设备：仅当 USB 实际在线（`d.usb`）时才提供「改 USB」，否则不显示——否则点击会因无 USB 连接报错。
- 真正的 USB 类型（`activate_via=usb` 且 USB 在线）设备才显示「USB 激活」。

判定逻辑（设备卡片非健康分支）：

```js
const netVia = d.activate_via === 'network' || !d.usb;
```

```bash
go test ./internal/gateway -count=1 -timeout 180s \
  -run 'TestProtocolCmd|TestChannelReachableForVia|TestUserStopped|TestWdaBaseURLFor|TestWdaProbeVia|TestConnTypeOfDevice|TestParseActivateVia'
```
