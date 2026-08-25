# 无线保活：usbmux ConnectionType=Network 自动修复（开发文档）

日期：2026-08-25

状态：已落地实现，功能可用；配套开关、手动修复、实时状态与单测齐备。

> 背景与排障过程见 [docs/testing/2026-08-25-connection-type-network-usbmuxd-restart-fix.md](../testing/2026-08-25-connection-type-network-usbmuxd-restart-fix.md)。

## 1. 功能目标

把“让插着 USB 的 iPhone 进入 usbmux `ConnectionType=Network`”这一动作，做成网关内置功能：

- **开启/关闭**：一个开关控制是否允许后台自动修复。
- **自动修复**：开关开启时，周期检测“USB 已接入但缺 `Network` 条目”的设备；一旦存在，自动重启系统 usbmuxd 触发网络发现，并等待 `Network` 条目重新出现。
- **手动修复**：页面提供“立即修复”，不受开关限制，随时可执行一次。
- **实时状态**：显示当前“已进入 Network / 总数 / 缺 Network 数 / 是否待修复 / 上次修复结果”。
- **修复判据**：同一 UDID 只要有任一 `Network` 条目即为“已进入 Network”；USB 行不覆盖 Network 行。

## 2. 名词与判定规则

| 名词 | 定义 |
|---|---|
| usbmux `Network` 条目 | `ios list --details`（或 `idevice_id -l -n`）里该 UDID 存在 `ConnectionType:"Network"`。 |
| 已进入 Network | `netSet` 含该 UDID。 |
| 缺 Network（待修复） | 该 UDID 在 `usbSet`（有 USB 条目）但不在 `netSet`。 |
| 可拔线（unplug-safe） | iOS≤16 有 `Network` 条目；iOS 17+ 有 go-ios 隧道。 |

同一 UDID 可同时有 USB 与 Network 两条（拔线前双通道），判定时 **Network 优先**。

## 3. 整条功能链路

```text
前置条件（设备+网络+权限）
   └─ 后台检测（每 30s）：读取 `ios list --details`
        └─ 存在待修复设备？
             ├─ 否 → 什么都不做
             └─ 是 → 开关开 + 过了冷却(2min)？
                  ├─ 否 → 跳过
                  └─ 是 → 重启 usbmuxd（sudo -n 或 osascript 授权）
                         └─ 等待最多 20s 轮询
                              └─ 全部 Network → 记录“已修复”
                                 否则 → 记录“仍有 N 台缺 Network”
```

手动“立即修复”同走“重启 usbmuxd → 等待校验”，但不受开关/冷却限制，且会提示弹管理员授权。

## 4. 前置条件（必须全部满足，否则功能要么不触发、要么触发了也不见效）

### 4.1 设备侧

- 每台 iPhone 已与同一台 Mac **配对并信任**（USB 连过一次、点过“信任”）。
- 手机 **解锁 / 亮屏**（处于可接受网络握手的状态）。
- `com.apple.mobile.wireless_lockdown` 域：
  - `EnableWifiConnections = true`
  - `EnableWifiDebugging = true`（**关键**：真正让 `62078` 服务于无线调试的关键开关）
  - `SupportsWifi / SupportsWifiSyncing = true`（只读能力位）

### 4.2 网络侧

- 手机与 Mac 在**同一个 Wi-Fi 网段**（本机 en0，如 `192.168.10.0/24`）。
- 手机 Wi-Fi AP **没有开“客户端隔离”**（否则组播到、单播超时）。
- `手机IP:62078` 从 Mac **单播可连通**。
- 不要走“只有 USB 个人热点链路（169.254.x）”来实现无线保活——那只是 USB 链路，不是 Wi-Fi 网段。

### 4.3 主机侧

- 系统 usbmuxd 为 Apple 的 `/var/run/usbmuxd`（launchd 守护进程），重启后会自动拉起。
- 网关进程可执行 `killall usbmuxd`（见 4.4），否则自动修复会在“权限”处失败、手动修复会弹管理员授权。
- `ios` / `ios.exe`（go-ios）在 PATH 或随包 fallback 中，`network_safety.go`/`usbmux_net.go` 依赖它枚举连接类型。

### 4.4 权限

- 授权脚本：`sudo sh scripts/setup-usbmux-sudo.sh`
- 写入 `/etc/sudoers.d/wda-gateway-usbmux`，仅放行 `/usr/bin/killall usbmuxd`（带参数匹配）。
- 未授权时自动修复会失败（记录 error），手动“立即修复”会走 `osascript` 弹一次管理员密码框。

## 5. 配置与持久化

- 存储：`<state>/gateway.db` 的 `config` 表，key=`usbmux_net`，值为 JSON。
- 字段：`{"auto_repair": bool}`（默认未开启）。
- 代码：`internal/gateway/config.go` 的 `UsbmuxNet UsbmuxNetConfig`，`SetUsbmuxNet(autoRepair bool)` 写库。

## 6. REST API

| 方法 | 路径 | 作用 | 请求体 | 返回 |
|---|---|---|---|---|
| GET | `/api/usbmux-net` | 状态+配置 | - | `usbmuxNetStatus` |
| PUT | `/api/usbmux-net` | 修改自动修复开关 | `{"auto_repair":true\|false}` | 更新后的状态 |
| POST | `/api/usbmux-net` | 手动立即修复 | - | `{"ok":true,"status":{...}}` |

`usbmuxNetStatus` 字段：

```json
{
  "auto_repair": false,
  "total": 3, "network": 1, "usb_only": 1, "absent": 1, "unplug_safe": 1,
  "needs_repair": false,
  "last_restart": "2026-08-25T00:59:00+08:00",
  "last_result": "ok · usbmuxd 已重启，全部 USB 设备均已挂上 Network 条目",
  "devices": [
    {"udid":"...","name":"iPhone Plus-2","ip":"10.0.0.1","connection":"Network","unplug_safe":true},
    {"udid":"...","name":"iPhone 7","ip":"10.0.0.2","connection":"UsbOnly","unplug_safe":false}
  ]
}
```

### 6.1 需人工处理的情况（接口返回错误）

- POST 返回 `500 {"error":"上一次修复仍在进行，请稍后再试"}` → 正在跑上一轮。
- POST 返回 `500 {"error":"重启 usbmuxd 需要 root 权限：…"}` → 未授权，先跑授权脚本或允许弹窗。

## 7. UI

`static/index.html` 增加“无线保活 · usbmux Network 自动修复”面板：

- 一个开关（`usbmuxSwitch`，onchange 即保存）。
- 状态文本（`usbmuxStatus`）：已进 Network / 总数、缺 Network 数、待修复、上次结果。
- 按钮：`立即修复`（POST）、`刷新`。
- 数据每 5 秒随 `refreshAll()` 自动刷新。

## 8. 代码文件清单

| 文件 | 职责 |
|---|---|
| `internal/gateway/config.go` | `UsbmuxNetConfig` + `SetUsbmuxNet` + load 读取 |
| `internal/gateway/usbmux_net.go` | 连接集读取、状态计算、重启 usbmuxd、自动/手动修复、后台循环 |
| `internal/gateway/usbmux_net_repair_unix.go` | macOS/Linux 重启 usbmuxd（root / sudo -n / osascript 授权） |
| `internal/gateway/usbmux_net_repair_windows.go` | Windows 重启 Apple 设备服务（需以管理员运行网关） |
| `internal/gateway/network_safety.go` | `parseUsbmuxConnectionTypes` 修正为 **Network 优先** |
| `internal/gateway/gateway.go` | Gateway 增加 usbmux 修复状态字段（冷却/结果/并发） |
| `internal/gateway/web.go` | `/api/usbmux-net` 三端点 |
| `cmd/gateway/main.go` | `go gw.UsbmuxNetLoop(ctx)` 启动后台循环 |
| `static/index.html` | 面板 + JS（load/save/repair） |
| `scripts/setup-usbmux-sudo.sh` | 免密 `killall usbmuxd` 授权 |
| `internal/gateway/usbmux_net_test.go` | 状态/判定/解析单测 |

## 9. 验收与复现

### 9.1 构建与测试

```bash
go build ./...
go test ./internal/gateway/   # 168 项通过
```

### 9.2 启用

1. 授权：`sudo sh scripts/setup-usbmux-sudo.sh`
2. 页面打开“无线保活”开关（或 `curl -X PUT localhost:8300/api/usbmux-net -d '{"auto_repair":true}'`）。
3. 触发：`curl -X POST localhost:8300/api/usbmux-net`（立即修复，可用来验证整条链）。

### 9.3 判定

```bash
curl -s localhost:8300/api/usbmux-net | jq
idevice_id -l -n | grep '(Network)'
```

- 修复前：某 UDID 只有 `(USB)`；修复后出现 `(Network)`（`needs_repair` 变 false）。

## 10. 注意事项 / 坑

1. **重启 usbmuxd 是全局动作**：会短暂断开所有 USB 设备再自动重连，不影响 Wi-Fi 上的 WDA/已激活会话；建议避开发送高峰，或触发后再确认一次激活状态。
2. **仅设 `EnableWifiConnections` 不够**：`EnableWifiDebugging` 才是真正打开 62078 的开关；本功能依赖它。
3. **手机不在 Mac 同网段**：即使开满开关、62078 也不通 → 修复无效（记录“仍有 N 台缺 Network”）。此时去查 Wi-Fi/AP 客户端隔离/个人热点。
4. **USB 行不会覆盖 Network 行**：`parseUsbmuxConnectionTypes` 已修正为 Network 优先生效，避免双通道时误判为“未保活”。
5. **冷却防抖**：两次自动修复至少间隔 2 分钟，自动修复失败（如权限）也会记录 result，下次过了冷却再试；手动修复不受限制。
6. **并发保护**：手动 + 自动不会同时执行第二次（`usbmuxRepairRun` 哨兵）。
7. **授权撤销**：`sudo rm -f /etc/sudoers.d/wda-gateway-usbmux`。

## 11. Windows 支持

检测、开关、状态展示与 macOS 共用。**修复动作不能照搬 `killall usbmuxd`。**

实机记录见 [docs/testing/2026-08-25-windows-connection-type-network.md](../testing/2026-08-25-windows-connection-type-network.md)。

### 11.1 前置条件（Windows）

- **只要桌面版 iTunes**（`C:\Program Files\iTunes\iTunes.exe`）+ **Apple Mobile Device Support**（服务 `Apple Mobile Device Service`，`:27015`）。
- **不要**同时装 Microsoft Store 版 iTunes：会与 Win32 AMDS 冲突，启动时要求卸载另一套，并可能占住 `:27015`。
- `ios.exe` / `tidevice` / `idevice_id` 读的是 AMDS，不是商店版进程。
- 手机：USB 配对、解锁、与 **这台 Windows** 同一 Wi-Fi；`wifi-lockdown` 写入 `EnableWifiConnections` + `EnableWifiDebugging`。

### 11.2 修复动作差异

- macOS/Linux：`restartUsbmuxd()` 重启系统 `usbmuxd`（可让 Network 出现）。
- Windows：`restartUsbmuxd()` **拒绝**重启 AMDS。本机实测 `sc stop/start` 不会挂上 Network，反而让 USB 从 usbmux 消失、设备列表被清空。

### 11.3 手动验证（Windows）

```bat
ios list --details
idevice_id -l
idevice_id -n
wifi-lockdown <udid>
```

不要用 `net stop/start "Apple Mobile Device Service"` 当修复手段。

### 11.4 实机结论（2026-08-25）

在桌面 iTunes 勾选「通过 Wi-Fi 同步」并应用后：插着 USB 只有 USB 行；拔线后 `-n` 仍空。`WirelessBuddyID` 仍指向 Mac。Windows 上不能把「修复」宣传成可拔线。

## 12. 回滚

- 把开关关掉（`{"auto_repair":false}`）即停止自动修复；手动修复仍可用。
- 代码回退：还原上述文件即可，无数据库结构变更（config 行删除即恢复默认）。
