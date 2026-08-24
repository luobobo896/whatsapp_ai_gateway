# ConnectionType=Network（usbmux / remotexpc）

日期：2026-08-24

## 协议事实

[appium-ios-remotexpc](https://github.com/appium/appium-ios-remotexpc) 的 `Connect` **没有** `ConnectionType` 字段，只有 `DeviceID` + `PortNumber`。

无线调试的做法是：`ListDevices` 里挑 `Properties.ConnectionType == Network` 的那一行，用它的 `DeviceID` 去 `Connect`。同一 UDID 的 USB 行是另一个 DeviceID。

本仓库 `wifi-runwda` 的 `pickDevice` / `chooseNetworkRoute` 按这个规则显式选 Network（remotexpc 默认还把 USB 排在 Network 前面，拔线场景不能照抄）。

## 本机实况

| UDID 前缀 | usbmux | idevice_id | WDA |
|---|---|---|---|
| 4886579a | DeviceID=101 **Network** | `-n` 有 | 192.168.10.237:8100 ready 15.8.8 |
| 5060c403 | DeviceID=111 **USB only** | `-l` 有、`-n` 无 | 192.168.10.192:8100 ready 15.8.7（USB XCTest） |

5060 侧：`EnableWifiConnections=true`，Bonjour `_apple-mobdev2` 有，`192.168.10.192:62078` 和 `fe80::1c81:c44c:8859:34d8%en0:62078` 都能连。Apple usbmuxd 仍然不挂 Network 行。

## 试过且失败的路径

对 5060 把 `Connect` 改成直连 Wi-Fi IP（假装 ConnectionType=Network）：

- lockdown（`ios info` / `ios date`）成功
- `runwda` 在 `installation_proxy` / testmanagerd 端口立刻 broken pipe
- 网关看护曾把这次失败误判成签名问题并冷却重激活

结论：没有 usbmuxd 的 Network DeviceID，不能用脚本“指定 ConnectionType=Network”拉起可拔线的 XCTest。

## 命令

```bash
idevice_id -l    # USB
idevice_id -n    # Network；5060 必须出现在这里才能无线激活
```
