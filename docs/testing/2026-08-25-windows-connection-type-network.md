# 2026-08-25 Windows 上 ConnectionType=Network 实机结论

日期：2026-08-25  
环境：Windows，桌面版 iTunes 12.13.10.3 + Apple Mobile Device Support 19.4.0.10；bundled go-ios 1.3.2、tidevice 0.12.11；真机 iPhone 7 Plus `4886579a…` / iOS 15.8.8  
对照：Mac 上 `killall usbmuxd` 可使同一批机出现 Network（见 [2026-08-25-connection-type-network-usbmuxd-restart-fix.md](./2026-08-25-connection-type-network-usbmuxd-restart-fix.md)）

## 一句话

`ConnectionType=Network` 是**本机 usbmux 设备表**里的一行，不是 WDA、不是 `:8100`、也不是手机开关。  
**Mac 能挂上；这台 Windows 在做完全部公开步骤后仍只有 USB。**

## 按系统：事实与步骤

### 手机（两端相同）

USB 插上、解锁、信任电脑后写入（本仓 `wifi-lockdown`）：

| 键（`com.apple.mobile.wireless_lockdown`） | 需要 |
|---|---|
| `EnableWifiConnections` | true |
| `EnableWifiDebugging` | true |
| `SupportsWifi` / `SupportsWifiSyncing` | 只读，能力位 |

本机回读已全部为 true。`WirelessBuddyID` 长期为 Mac 的 `514FF52E-…`，**不是**这台 Windows 的 `HostID`。从 Windows 再写一遍开关，Buddy **不会**改成 Windows。

`:8100` / `:62078` 通只说明机上 HTTP / lockdown 在听，**不等于**本机 usbmux 有 Network。浏览器打开 `http://<ip>:8100/` 会 `unknown command`；探活用 `/status`。

### macOS

1. USB 配对、同一 Wi-Fi、上述两个开关为 true。  
2. `sudo killall usbmuxd`（launchd 会拉起并重新做 USB + 网络发现）。  
3. 验证：

```bash
idevice_id -l -n | grep '(Network)'
ios list --details          # 应同时有 USB 与 Network
```

有 Network 后再 `ios runwda` / 网关激活，拔 USB 后 XCTest 可不拆。停会话：在**当初激活的那台 Mac**上停，或按手机两边音量键。Windows 点停止杀不到 Mac 拉起的进程。

### Windows（本机实测）

**不要**把 Mac 的「重启 usbmuxd」照搬成重启 Apple 服务。

| 做过的步骤 | 结果 |
|---|---|
| `idevicepair validate` | SUCCESS |
| `wifi-lockdown` 写两个开关 | 成功，Buddy 仍是 Mac |
| `sc stop/start "Apple Mobile Device Service"` | **不会**出 Network；usbmux 变空；设备列表被当成掉线清掉 |
| 官网 libimobiledevice 再下一套 idevice* | 只是客户端，读同一 AMDS，变不出 Network |
| go-ios 已是官方 latest 1.3.2 | 只 `ListDevices`，不创建 Network |
| 补装 tidevice 0.12.11 | `ConnType` 仍是 USB；官方还要求 iTunes |
| 商店版 iTunes | 与 Win32 AMDS **冲突**，启动会要求卸载另一套；还会占 `:27015` |
| 桌面版 iTunes + 勾选「通过 Wi-Fi 同步」并应用 | 插着 USB：30–40s 只有 USB；**拔线后 `-n` 仍空** |

正确组件组合（本机最终状态）：

1. **只留桌面版 iTunes**（`C:\Program Files\iTunes\iTunes.exe`），卸掉商店版。  
2. 保留 **Apple Mobile Device Support**（服务名 `Apple Mobile Device Service`，听 `127.0.0.1:27015`）。  
3. go-ios / tidevice / `idevice_id -l` `-n` 读这个服务。

Windows 网关「立即修复」：**拒绝重启 AMDS**，返回明确错误，避免再清设备列表。

验证命令：

```bat
ios list --details
idevice_id -l
idevice_id -n
tidevice list
wifi-lockdown <udid>
```

期望（本机未达到）：`-n` 出现同一 UDID，或 `ios list` 出现 `"ConnectionType":"Network"`。  
本机达到的是：USB 可见、配对 SUCCESS、开关 true、**没有 Network**。

## 代码改动（本次提交）

- `cmd/wifi-lockdown`：同时写 `EnableWifiConnections` + `EnableWifiDebugging`，打印 `WirelessBuddyID`。  
- `internal/gateway/usbmux_net_repair_windows.go`：不再 `sc stop/start`。  
- Windows 点修复时返回：重启 AMDS 不会出 Network，反而会弄丢 USB。

## 日常怎么用（在 Network 做不出来时）

- Windows：**插着 USB** 激活、发送。  
- 需要拔线保活：在 **Mac** 上激活（usbmux 已有 Network），机上 WDA 再走 `http://<ios.ip>:8100/status`。  
- Windows 停止按钮停不掉 Mac 拉起的 Automation Running，用 Mac 停或音量键。
