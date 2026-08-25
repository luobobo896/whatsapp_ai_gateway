# USB / Network 激活与首次授权

日期：2026-08-25  
最后验证：2026-08-25，`go test ./internal/gateway` 通过。

USB 与 Network 是两条独立通道：互斥、不混用、不自动兜底。  
**Mac 与 Windows 业务规则相同**（首次授权、通道互斥、发现、停止）。平台只差实现：usbmux 套接字 / `:27015`、iproxy 端口参数写法、Mac 可 `killall usbmuxd` 而 Windows 不能照搬重启 AMDS。

## 首次授权（写无线开关）

写 `EnableWifiConnections` / `EnableWifiDebugging` **只允许 USB**：

1. 必须插着 USB。
2. 手机已设置锁屏密码，场内统一为 `0000`（四个零）。
3. 管理页点「首次授权」：弹框说明要求并输入 `0000` 确认，
   再 `POST /api/devices/{udid}/authorize-wifi`（请求体 `{"passcode":"0000"}`，后端校验）。
4. 后端先 `wifi-lockdown -status` 读 `EnableWifiConnections` / `EnableWifiDebugging`：
   - 两开关均 `true` → 跳过写入，直接标记已授权（幂等，不重复弹手机密码框）；
   - 否则 `wifi-lockdown` 写开关，手机会弹密码框，在手机上输入 `0000` 完成。

没有 USB，授权接口失败。Network 激活不写这两个开关。未授权则 Network 按钮不可用。

## 激活 / 停止 / 探活

Network 激活前，管理页弹出密码输入框，输入 `0000` 确认后调用
`POST /api/devices/{udid}/activate?via=network`（请求体 `{"via":"network","passcode":"0000"}`），
后端对 `via=network` 校验 `passcode` 必须为 `0000`，通过后才拉起 WDA。
USB 激活不要求该密码框。停止操作关闭主机 WDA 进程并拆除本通道隧道。

| 动作 | USB | Network |
|---|---|---|
| 激活 | 必须 USB 在线；`ios runwda` / xcodebuild / tidevice | 必须已 USB 授权；`wifi-runwda -require-network`（iOS 15–16） |
| 探活 / 发送 | 只走 USB iproxy | 只走 iproxy `-n` 或已记录 Wi-Fi IP |
| 看护重激活 | 只在 USB 仍在时 | 只在已授权且 Network/Wi-Fi 可达时 |
| 停止 | 杀主机进程 + 拆本通道隧道 + 不再用残留 `/status` 救活 | 同左 |

明确拒绝：Network 激活改走 USB、USB 激活改走 Wi-Fi、iOS 17+ Network 改走 go-ios 隧道、tidevice/xcodebuild 当 Network。

## 发现

`Discover()` 合并 `idevice_id -l -n` 的 USB 与 Network。`USBUDIDs()` 仍只返回 USB。

## 测试

```bash
go test ./internal/gateway -count=1 -timeout 180s
cd cmd/wifi-lockdown && go test -count=1
```

相关测试记录：

- [docs/testing/2026-08-25-first-connect-wifi-auth.md](../testing/2026-08-25-first-connect-wifi-auth.md)
- [docs/testing/2026-08-25-usb-network-exclusive-activate.md](../testing/2026-08-25-usb-network-exclusive-activate.md)
- [docs/testing/2026-08-25-gateway-network-discovery.md](../testing/2026-08-25-gateway-network-discovery.md)
