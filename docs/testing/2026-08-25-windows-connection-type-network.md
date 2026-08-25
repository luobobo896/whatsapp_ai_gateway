# 2026-08-25 Windows 上 ConnectionType=Network：按系统步骤

日期：2026-08-25  
环境：Windows，桌面版 iTunes 12.13.10.3 + Apple Mobile Device Support 19.4.0.10；bundled go-ios 1.3.2、tidevice 0.12.11；真机 iPhone 7 Plus `4886579a…` / iOS 15.8.8 / `192.168.10.237`  
对照：Mac 上 `killall usbmuxd` 可使同一批机出现 Network（见 [2026-08-25-connection-type-network-usbmuxd-restart-fix.md](./2026-08-25-connection-type-network-usbmuxd-restart-fix.md)）

## 一句话

`ConnectionType=Network` 只看**本机 usbmux `ListDevices`** 是否出现 `Network` 行。  
WDA `:8100`、lockdown `:62078`、iTunes「通过 Wi-Fi 同步」、Bonjour `_apple-mobdev2` **都不是** Network。  
**Mac 能挂上；这台 Windows 按下面清单做完后，插着 USB 仍只有 USB 行。**

验收命令（必须同时满足才算做成）：

```bat
idevice_id -n
ios list --details
```

`-n` 打出同一 UDID，或 JSON 里出现 `"ConnectionType":"Network"`。  
`http://192.168.10.237:8100/status` 通、`Test-NetConnection … 62078` 通，都不能结案。

---

## 1. 手机

USB 插入、解锁、点「信任此电脑」。与 Windows 同一 Wi-Fi（本机 `192.168.10.237`）。

用本仓 `tools\wifi-lockdown.exe <udid>` 写并回读 `com.apple.mobile.wireless_lockdown`：

| 键 | 需要 | 本机 |
|---|---|---|
| `EnableWifiConnections` | true | true |
| `EnableWifiDebugging` | true | true |
| `SupportsWifi` / `SupportsWifiSyncing` | 只读能力位 | true |
| `WirelessBuddyID` | 等于**这台 Windows iTunes** 的 `WirelessBuddyID` | 已对齐 `31274029-17647842852914356048` |

三个容易混的 ID：

| ID | 来源 | 本机值 |
|---|---|---|
| pair `HostID` | `C:\ProgramData\Apple\Lockdown\<udid>.plist` | 曾为 `31273522-368570121651719656` |
| iTunes `WirelessBuddyID` | `%APPDATA%\Apple Computer\Preferences\com.apple.iTunes.plist` | `31274029-17647842852914356048` |
| 手机 `WirelessBuddyID` | lockdown 域 | 必须写成 iTunes 那个；勾选 Wi-Fi 同步**不会**改它 |

只写两个 Enable 开关、不改 Buddy 时，手机 Buddy 会一直留着 Mac 的 `514FF52E-…`。  
`wifi-lockdown` 现在：优先用 iTunes Buddy；不一致就先关 `EnableWifiConnections`、写入 Buddy、再打开两个开关；已对齐时也会 bounce 一次连接，刷新 Bonjour。

手机侧网络现象（本机已核实）：

- 组播 `_apple-mobdev2._tcp.local` 有应答，来自 `192.168.10.237:5353`。
- PTR 实例：`a0:3b:e3:a0:e0:e3@fe80::…._apple-mobdev2._tcp.local`（MAC 与 pair 记录 `WiFiMACAddress` 一致）。
- SRV 宣告端口 **32498**，该端口 **TCP 不通**。
- lockdown **`:62078` TCP 通**。
- WDA **`:8100` 不通**（这台 Windows 没在无线上拉起 WDA）。
- 浏览器打开 `http://<ip>:8100/` 会返回 WDA `unknown command`；探活只用 `/status`。

---

## 2. macOS（对照，已成功）

1. USB 配对、同一 Wi-Fi、上一节两个 Enable 为 true。  
2. `sudo killall usbmuxd`（launchd 拉起后重新做 USB + 网络发现）。  
3. 验证：

```bash
idevice_id -l -n | grep '(Network)'
ios list --details
```

有 Network 后再 `ios runwda` / 网关激活，拔 USB 后 XCTest 可不拆。  
停会话：在**当初激活的那台 Mac**上停，或按手机两边音量键。Windows 的停止按钮杀不到 Mac 拉起的进程。

---

## 3. Windows：组件（缺一不可，且不能混装）

按顺序装，**不要**商店版和桌面版并存。

| 步骤 | 做什么 | 为什么 | 本机结果 |
|---|---|---|---|
| 3.1 | 卸掉 Microsoft Store 版 iTunes | 它用 `AppleMobileDeviceProcess` 占 `:27015`，和 Win32 AMDS 互斥，启动会要求卸载另一套 | 已卸 |
| 3.2 | 装桌面版 iTunes | 路径必须是 `C:\Program Files\iTunes\iTunes.exe` | 12.13.10.3 |
| 3.3 | 确认 Apple Mobile Device Support | 服务名带空格：`Apple Mobile Device Service`（`sc query "Apple Mobile Device Service"`）。**不是** `Apple.AppleMobileDeviceSupport`（那个名字会 1060） | 19.4.0.10，RUNNING，`127.0.0.1:27015` |
| 3.4 | 管理员启动该服务 | 无管理员时 `sc start` 拒绝；服务停了 usbmux 全空 | 需 UAC |
| 3.5 | 物理拔插 Lightning | 新装/重启 AMDS 后，即使用户层 PnP 仍显示 iPhone，usbmux 也可能是空的 | 必须拔插，软禁用 PnP **掉不了** usbmux USB 行 |
| 3.6 | go-ios / tidevice / libimobiledevice `idevice_*` | 全是 **AMDS 客户端**，不能创建 Network 行 | go-ios 1.3.2、tidevice 0.12.11；官网 libimobiledevice **没有** Windows 官方包，且写明 Windows 要靠 AMDS |

iTunes 12.13 自带进程内 `dnssd.dll` / `mDNSResponderDLL.dll`（在 `C:\Program Files\iTunes\`）。  
AMDS 目录和 `System32` **没有** `dnssd.dll`。本机也没有独立 Bonjour 服务（无 `mDNSResponder.exe`）。  
`MobileDevice.dll` 字符串里有 `Bonjour`、`_apple-mobdev`。iTunes 自己在 `192.168.10.101:5353` 听 mDNS；AMDS 不听 5353。

**禁止**：`sc stop/start "Apple Mobile Device Service"` 当「修复」。本机实测不会出 Network，USB 从 usbmux 消失，网关会把设备当掉线清掉。

---

## 4. Windows：配对与 iTunes Wi-Fi 同步

1. 手机解锁、信任；`idevicepair validate -u <udid>` → `SUCCESS`。  
2. 打开桌面 iTunes，左侧选中这台 iPhone（摘要页）。  
3. 「选项」勾选 **通过 Wi-Fi 与此 iPhone 同步**，点应用/完成。  
4. 本机 UI 已核实：该勾已打上；`MobileDeviceWakeupTokens` 已有该 UDID，服务名与 `_apple-mobdev2` 实例一致。  
5. 跑 `wifi-lockdown <udid>`，直到打印的 `WirelessBuddyID` 等于 iTunes plist 里的值，且 `rebind=false`。

勾选 Wi-Fi 同步 **≠** usbmux Network。它只让 iTunes 自己走无线同步；`ListDevices` 仍可以只有 USB。

---

## 5. Windows：验证（每一步都要跑）

```bat
ios list --details
idevice_id -l
idevice_id -n
tidevice list
wifi-lockdown <udid>
```

| 看到 | 含义 |
|---|---|
| `"ConnectionType":"USB"` 且 `-n` 空 | **还没有 Network** |
| `-n` 有 UDID / `"ConnectionType":"Network"` | 做成了，才能谈拔线保活 |
| `:62078` 通 | 机上 lockdown 在听，不是 usbmux Network |
| `:8100/status` 通 | 机上 WDA HTTP，不是 usbmux Network |
| `:8100/` 返回 `unknown command` | 正常，不要改成这个地址当连接 |

插着 USB 时只有 USB 行是常见的。**必须再物理拔线**（保持解锁、Wi-Fi、iTunes 开着）立刻再跑 `-n`。  
软禁用 Apple USB PnP 设备：**不能**让 usbmux 丢掉 USB 行，不能代替拔线。

Buddy 改成 iTunes ID **之前**拔过一次：`-n` 仍空。  
Buddy 对齐之后：`Ctrl+E` 没弹出；COM `EjectIPod()` 让 iPhone 从 iTunes 源列表消失 40s，**没有**从 Wi-Fi 回来；usbmux 全程仍是 USB，`-n` 空（线还插着）。软弹出 ≠ Network。物理拔线观察仍待做。

---

## 6. 本机试过、无效的动作

| 动作 | 结果 |
|---|---|
| `idevicepair validate` | SUCCESS，无 Network |
| 只写两个 Enable 开关 | 成功，Buddy 仍是 Mac |
| 把 Buddy 重绑成 iTunes `WirelessBuddyID` | 回读已对齐；usbmux 仍 USB |
| Buddy 对齐后再 bounce `EnableWifiConnections` | `_apple-mobdev2` 仍宣告关闭的 32498 |
| `sc stop/start` AMDS | 列表被清空，无 Network |
| 再下一套 idevice* / 已是最新 go-ios / 补 tidevice | 只读同一 AMDS |
| 商店版 iTunes | 与 AMDS 冲突 |
| 桌面 iTunes 勾 Wi-Fi 同步并应用 | UI 已勾；插线 30–40s 仍 USB；对齐前拔线 `-n` 空 |
| 软 Disable-PnpDevice | usbmux 仍报 USB |
| iTunes COM `EjectIPod()` | iTunes 里设备消失 40s 未从 Wi-Fi 回来；usbmux 仍 USB，`-n` 空 |

Windows 网关「立即修复」：**拒绝重启 AMDS**，返回明确错误。

---

## 7. 日常怎么用（在这台 Windows 还没有 Network 时）

- Windows：**插着 USB** 激活、发送。  
- 需要拔线保活：在 **Mac** 上激活（那边 usbmux 已有 Network）。  
- Windows 停止按钮停不掉 Mac 拉起的 Automation Running，用 Mac 停或音量键。

## 代码

- `cmd/wifi-lockdown`：按 iTunes `WirelessBuddyID` 重绑；已对齐时也 bounce 无线连接。  
- `internal/gateway/wireless_buddy.go`：`needsWirelessRebind` / `preferredWirelessBuddyID`。  
- `internal/gateway/usbmux_net_repair_windows.go`：不 `sc stop/start`。
