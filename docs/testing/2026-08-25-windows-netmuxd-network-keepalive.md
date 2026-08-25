# Windows 无线 WDA 拔线保活（netmuxd 方案）

日期：2026-08-25
环境：Windows + AMDS（Apple Mobile Device Service）；iPhone 7 Plus `4886579a…` / iOS 15.8.8 / Wi-Fi `192.168.10.237`
结论：netmuxd 已替代 `usbmux-bridge` 集成进网关并打包部署；纯 Wi-Fi 无线激活 WDA 可存活约 8 分钟
（此前 60~80 秒必断），剩余断开点是 iOS 空闲/锁屏主动关闭无线调试会话，需要手机保持亮屏。
“插 USB 激活 → 拔线保持在线”的最终验收待手机重新广播 mDNS 后复测（见下方状态）。

## 根因

之前 `usbmux-bridge` 只把 usbmux `Connect` 直接 TCP 转发到手机 Wi-Fi IP，缺少 iOS 网络
lockdown 会话的保活机制：

- 无线拉起 WDA 后约 60~80 秒，手机上 testmanagerd 连接被 iOS 掐掉
  （`conn closed unexpectedly EOF`），WDA 进程 `exit status 1`，拔线即断；
- macOS 上由 usbmuxd 维护持久 network session，Windows 上 AMDS 没有等价能力。

## 方案：netmuxd（shim 模式）

`jkcoxson/netmuxd` v0.4.3（LGPL-2.1，`tools/netmuxd.exe`）原生实现 Apple usbmuxd：

1. mDNS 发现 `_apple-mobdev2._tcp.local` 无线设备；
2. 用 `C:\ProgramData\Apple\Lockdown\<UDID>.plist` 配对记录建立
   `com.apple.mobile.heartbeat` 会话（MARCO/POLO 约每 10 秒一次），heartbeat 失败才摘除设备；
3. `--upstream-usbmuxd 127.0.0.1:27015`（shim 模式）：USB 请求原样转发 AMDS，
   Network 设备由 netmuxd 自己管理，一个端口同时呈现 USB + Network，与 Mac 同一套业务规则。

网关启动命令：

```bat
netmuxd.exe --port 27016 --upstream-usbmuxd 127.0.0.1:27015
set USBMUXD_SOCKET_ADDRESS=tcp://127.0.0.1:27016
```

不杀 AMDS、不重启 Apple 服务（USB 设备不会从列表消失）。

## 网关集成

- `internal/gateway/netmuxd.go`：Windows 上自动拉起 / 看护重启 / 退出清理 netmuxd，
  并注入 `USBMUXD_SOCKET_ADDRESS`（原 `usbmux_bridge.go` 被替换删除）。
- `internal/gateway/netmuxd_other.go`：非 Windows 平台提供同签名 no-op 桩，
  `netmuxd.go`/`netmuxd_test.go` 带 `//go:build windows`，保证 Mac 交叉编译不受
  `x/sys/windows` 影响（已用 `GOOS=darwin go build ./...` 验证）。
- `wda_activate.go`：Windows + netmuxd 生效时，Network 激活直接 `ios runwda`
  （无线 testmanagerd），不再套 wifi-runwda 双层代理。
- `web.go` / `watchdog.go`：netmuxd mDNS 发现的 Network 设备视为在线（列表可见、防误删）；
  看护的在线集合合并 go-ios 的 `ios list --details`（`usbmuxNetworkUDIDs`，1s 缓存），
  `hasMuxNetwork` 在 Windows 上同样改走该来源（idevice_id 读不到 netmuxd）。
- `device_prune.go`：`wifi_debug=true` 的无线设备掉线只隐藏、不物理删除，
  避免 iOS 空闲关闭会话后配置丢失、手机醒来自动重激活时还要重新 USB 授权。
- `watchdog.go`：Windows + netmuxd 下，Network 重激活必须存在 usbmux Network 条目
  （mDNS），仅 IP 可 ping（手机熄屏但 lockdownd :62078 还开着）不再触发空转重激活。
- `scripts/build-windows-exe.sh`：把 `tools/netmuxd.exe`（含 LGPL-2.1 许可）分发到 `bin/`。
- 配对记录依赖 `C:\ProgramData\Apple\Lockdown`（首次 USB 授权生成，与 idevicepair/go-ios 共用）。

## 实测边界（USB 全程拔除，纯 Wi-Fi）

```text
USBMUXD_SOCKET_ADDRESS=tcp://127.0.0.1:27016 ios list --details
  → {"deviceList":[{"Udid":"4886…","ConnectionType":"Network",...}]}
ios runwda --udid=4886… --bundleid=com.wda.WebRunner.xctrunner
      --testrunnerbundleid=com.wda.WebRunner.xctrunner
      --xctestconfig=WebDriverAgentRunner.xctest
  → Process started successfully pid=69568
ios forward --udid=4886… 8100 8100
curl http://127.0.0.1:8100/status → {"ready":true,"message":"WebDriverAgent is ready…"}
```

两次完整复现（13:58、14:13 启动）均为 **约 7.5~8 分钟**后手机侧同时关闭：

- netmuxd heartbeat：`UnexpectedEof` / `Timeout`；
- testmanagerd：`conn closed unexpectedly EOF`（WDA 进程退出）；
- mDNS：`_apple-mobdev2._tcp` 广播消失，netmuxd 摘除设备。

关键观察：

- 断开前 `192.168.10.237:62078` 一直可达；断开后该端口仍可能开着（lockdownd 无线服务存活），
  但 mDNS 不广播 → netmuxd 建不出 Network 条目，`ios list --details` 为空。
- WDA 监听手机 loopback :8100，直连 `http://<手机IP>:8100/status` 不通属正常，
  必须经 netmuxd 隧道访问；判断 WDA 是否在跑应以 `ios list --details` 为准。
- 手机重新广播后 netmuxd 能立即重新发现（`--port 27018/27019` 实例均验证过）。

结论：iOS（尤其 iOS 15）在空闲/锁屏后会主动关闭无线调试会话，netmuxd 机制正确但受此系统行为限制。
长时间运行需保持手机亮屏（设置 → 显示与亮度 → 自动锁定 = 永不），并保持同一 Wi-Fi。

## 当前状态（2026-08-25 14:42 起）

- 已打包部署 `dist/windows-amd64`：网关自动拉起
  `netmuxd.exe --port 27016 --upstream-usbmuxd 127.0.0.1:27015`，日志确认 `netmuxd started`；
  旧 `usbmux-bridge.exe` 已从 `bin/` 删除。
- 设备配置已按“无线设备不删除”逻辑保留；手机唤醒并广播后，网关可自动重新发现并重激活，
  无需再次 USB 授权。
- 待验收：手机插 USB → 首次授权/激活（Network）→ 拔线 → 观察 WDA 保持在线。
  当前阻塞点：手机 mDNS 未广播（62078 可达但无广播），需要手机物理唤醒。

## 构建与测试

- `GOOS=windows` / `GOOS=darwin` 的 `go build ./...`、`go vet` 均通过（Mac 构建已修复）。
- 新增/修正单测：`TestNetmuxdArgsShimMode`、`TestProtocolCmdWindowsNetmuxdNetworkUsesGoIOS`、
  `TestChannelReachableForVia`（Windows netmuxd 需 Network 条目）、
  `TestUSBTunnelsToDropResetsWhenRediscovered`（udidKey 对齐）、
  `TestPruneOfflineDevicesRemovesOnlyAbsent`（wifi_debug 保留）均通过。
- 已知遗留：Windows 全量单测仍有历史 TempDir 清理失败（`results.db`/`sessions.db`
  测试未 Close，Windows 上删除被占用），与本次改动无关；打包按脚本开关 `SKIP_TESTS=1` 跳过。

## 限制

- 配对记录必须存在（首次无线授权仍走 USB）。
- 手机需与本机同一 Wi-Fi，且已开启无线调试
  （`EnableWifiConnections` / `EnableWifiDebugging`，由 `wifi-lockdown` 在 USB 下写入）。
- 无线通道依赖 iOS「本地网络」权限与 mDNS 可达性；Windows 的 `idevice_id`
  （libimobiledevice）不读 `USBMUXD_SOCKET_ADDRESS`，Network 相关能力走 go-ios/netmuxd。
- 手机锁屏/熄屏后 iOS 会关闭无线调试会话（约 8 分钟周期），必须保持亮屏才能长期在线。
