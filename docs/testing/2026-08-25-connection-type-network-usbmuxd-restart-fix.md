# ConnectionType=Network 修复实录：重启系统 usbmuxd

日期：2026-08-25

状态：✅ 两台 iPhone 均已进入 `ConnectionType=Network`

> 前置背景见 [2026-08-24-connection-type-network.md](2026-08-24-connection-type-network.md)。

## 一图流结论

**手机侧一切就绪 ≠ usbmuxd 会挂上 Network。** 设备侧无线开关已开、62078 也通、Bonjour 也广播，
但主机侧的系统 usbmuxd（Apple 的 `/var/run/usbmuxd`）没有重新做网络发现，始终只登记 USB 条目。
**重启一次 usbmuxd，让它重新发现，即可让同一个 UDID 出现 `Network` 条目。**

```bash
# 关键一步（需 sudo）
sudo killall usbmuxd        # launchd 会自动重新拉起
sleep 6                     # 等待设备重新枚举 + 网络发现
idevice_id -l -n | grep '(Network)'    # 出来即成功
```

## 问题

两台设备（`4886579a` iPhone Plus-2 / `5060c403` iPhone 7，iOS 15.8.x）USB 接入本机，
目标是把它们变成 `ConnectionType=Network`（无线调试通道），以便拔 USB 后自动化不拆。

但反复验证始终是：

```text
idevice_id -l -n        → 两行都是 (USB)
idevicepair -n -u <u> validate   → No device found
ios list --details      → "ConnectionType":"USB"
```

## 排查过程与关键发现

### 1. 设备侧（已做，且必要）

向 `com.apple.mobile.wireless_lockdown` 域写入并回读：

| 键 | 值 | 说明 |
|---|---|---|
| `EnableWifiConnections` | true | “在 Wi-Fi 上显示此 iPhone” |
| `EnableWifiDebugging` | true | **真正让 62078 服务于无线调试**（缺失/为 false 是常见坑） |
| `SupportsWifi` / `SupportsWifiSyncing` | true | 只读能力位 |

### 2. 网络侧（其实一直是通的）

- 手机 Wi-Fi 和 USB 链路两边的 `62078` 全部可连接：
  `192.168.10.237`、`192.168.10.148`、`169.254.118.79`、`169.254.254.103`。
- Bonjour `_apple-mobdev2._tcp` 广播正常，解析为 `iPhone-Plus-2.local:62078` / `iPhone-7.local:62078`。
- 手机上的 WDA 已能在 Wi-Fi IP 上返回 `WebDriverAgent is ready`。

### 3. 主机侧（根因）

- `/var/db/lockdown` 为空；`~/Library/Lockdown`、`~/.libimobiledevice` 均不存在。
- 配对记录由 Apple 的 usbmuxd 管理；系统 usbmuxd 未重新做网络发现，
  因此**从未给这两个 UDID 生成 `Network` 的 DeviceID**。
- `idevicepair -n` / `idevice_id -n` 依赖的就是 usbmuxd 的“网络设备列表”，
  列表里没有 → 报 `No device found`。这与 libimobiledevice issue #919 一致：
  macOS 上无线设备列表只由 Apple usbmuxd 提供，libimobiledevice 无法自行创建。

> 结论：**这是主机侧 usbmuxd 状态问题，不是 libimobiledevice 参数、不是手机、不是网络可达性。**

## 修复步骤（可重复执行）

1. 确认手机已 USB 配对、已解锁、与 Mac 同一 Wi-Fi，且 `EnableWifiConnections` / `EnableWifiDebugging` 均为 true。
2. 重启系统 usbmuxd：

   ```bash
   sudo killall usbmuxd
   sleep 6   # 等它被 launchd 拉起，并完成 USB + 网络发现
   ```

3. 验证：

   ```bash
   idevice_id -l -n | grep '(Network)'
   idevicepair -n -u <udid> validate        # 应为 SUCCESS
   ios list --details                       # ConnectionType:"Network"
   sh scripts/ios-network-pair.sh           # 项目自带检测
   ```

## 验证结果（本次实测）

重启后：

```text
idevice_id -l -n:
  5060c403... (Network)
  4886579a... (Network)
  5060c403... (USB)
  4886579a... (USB)

idevicepair -n -u 4886579a validate → SUCCESS
idevicepair -n -u 5060c403 validate → SUCCESS

ios list --details → 两个 UDID 均 "ConnectionType":"Network"（同时保留 USB 条目）

scripts/ios-network-pair.sh:
  4886579a...  USB ✓  Network ✓  iPhone Plus-2
  5060c403...  USB ✓  Network ✓  iPhone 7
  ✓ 已有 2 条 Network 连接。
```

此时设备状态为 **USB + Network 并存**——正是“拔线安全”状态：网关用 `usbmuxNetworkUDIDs()` 判定
`unplugSafeFor=true`，拔线后 `Automation Running` 不拆。

## 是否可复现

**可复现。** 条件与步骤：

- 前置：手机已配对、已解锁、与 Mac 同一 Wi-Fi 网段、`EnableWifiConnections=true` +
  `EnableWifiDebugging=true`、`手机IP:62078` 可连通、Bonjour 有广播。
- 触发：`sudo killall usbmuxd` 让系统 usbmuxd 重新发现。
- 复现判据：`idevice_id -l -n | grep '(Network)'` 从无到有。

若某天某台设备又只剩 `(USB)`，通常是 usbmuxd 没重新发现网络设备，重跑上面一步即可。

## 注意事项

1. `killall usbmuxd` 会短暂断开所有 USB 设备并自动重连；正在跑 WDA/自动化时会有瞬时抖动，
   建议在非发送高峰执行，或重激活一次即可恢复。
2. 设备侧的两个无线开关是**一次性写入**（Apple 记在设备上）；以后无需重复设置。
3. 不要用“直连 Wi-Fi IP 假装 Network”的方式（见 08-24 文档）：没有 usbmuxd 的 Network DeviceID，
   `runwda` 会在 `installation_proxy`/testmanagerd 端口 broken pipe。

## 常见坑总结

| 层 | 常见错误 | 正解 |
|---|---|---|
| 设备侧 | 只设 `EnableWifiConnections`，没设 `EnableWifiDebugging` | 两个都要 true（`EnableWifiDebugging` 是真正开 62078 的关键） |
| 网络侧 | 手机只在 USB 热点链路（169.254.x），没进 Mac 同网段 | 关 USB 热点，手机 Wi-Fi 连到 Mac 同一 SSID，AP 不隔离客户端 |
| 主机侧 | 设备能连 62078 仍不挂 Network | **重启系统 usbmuxd**（`sudo killall usbmuxd`） |
