# 2026-08-22 激活后拔 USB，WDA 走 Wi-Fi 保活

## 现象

激活 WDA 后拔掉 USB，网关把设备打成离线，群发停。竞品视频（`微信视频2026-08-22_091234_651.mp4`）里拔线后屏幕仍是 Automation Running，WhatsApp 还能继续打字。

## 根因

先前把 Mac 上的激活进程（xcodebuild / `ios runwda`）当成 WDA 是否存活。拔线后该进程 exit 75，云状态要求 `Running()`，即使手机 `:8100` 还在答 `/status` 也报 offline。40 位 UDID 还会写「需 USB、不支持 Wi-Fi」，不再探活。

事实拆开：

- **首次拉起** 40 位 UDID 仍要 USB（没有 CoreDevice 无线配对）。
- **保活/发送** 看机上 HTTP。XCTest 已经起来后，拔线只丢掉 iproxy 和主机进程，不该当 WDA 已死。

## 修复

- 云状态：`/status` 通 → `online`，不依赖主机进程。
- 本地列表 `wda_running`：主机进程或健康探活。
- 看护：拔线后继续用 Wi-Fi IP 探活；40 位机只跳过「无 USB 重拉起」，不改写健康态。
- 自报 `ios.ip` 在 USB / Wi-Fi 两条路径都写入，发送走 `wdaBaseURLFor`（无隧道即 Wi-Fi）。
- 主机进程 exit 75 不算崩溃冷却。

## 验证

```bash
go test ./internal/gateway/ ./internal/wda/ -count=1
```

```text
ok  wda-farm-gateway/internal/gateway
ok  wda-farm-gateway/internal/wda
```

手工：USB 激活 → 等设备显示在线且有 Wi-Fi IP → 拔线 → 屏幕仍 Automation Running → 网关仍在线、conn=Wi-Fi → 云平台再下发应继续发。

## 2026-08-22 21:26 复测（236 拔线）

现象不是「手机 Wi-Fi 掉了」：

| 检查 | 236（已拔） | 237（仍插 USB） |
|---|---|---|
| ping | 通（16–52ms） | 通 |
| `:8100 /status` | 连接拒绝 | 200，`ready` |
| USB | 不在 `idevice_id` | `4886579a…` |

236 的 xcodebuild 日志：

```text
Lost connection to DTServiceHub
testRunner encountered an error (... Lost connection to DTServiceHub)
** TEST EXECUTE FAILED **
```

主机进程 exit 65。XCTest 被 Xcode 测试会话拆掉，所以 HTTP 没了。本机对 237 再试 `kill -9 xcodebuild`：USB 还插着，`:8100` 同样立刻没了。xcodebuild 活着，测试会话才活着。

IPA 不是这次的根因：

- `~/Library/Application Support/WDAFarmGateway/wda.ipa` 与 `dist/wda.ipa` 都是 8-22 打的 zip
- 包里 `WebDriverAgentRunner` / `WebDriverAgentLib` 文件日期是 **8-19 11:47**
- 237 `/status` 自报 `Built at Aug 19 2026 11:47:02`
- Mac 激活日志是 `reuse existing WDA product` + `xcodebuild test-without-building`，**没有走 IPA / `ios runwda`**
- 本机没有 `ios` / `tidevice`，`auto` 回退 xcodebuild

竞品 Windows 是「已装 Runner + `ios runwda`」，不是 Xcode 测试会话。换一份同内容的 IPA 解决不了 DTServiceHub 拆会话。下一步是 Mac 也改成 go-ios/tidevice 拉起。

## 2026-08-22 21:45 已装 go-ios

- 来源：`gh release download v1.3.2 --repo danielpaulus/go-ios` 的 `go-ios-mac.zip`（官方 1.3.2，arm64+x86_64）
- 放到：`tools/ios`、`/Applications/WDAFarmGateway.app/Contents/Resources/bin/ios`（已 ad-hoc 签名）
- `build-dmg.sh` 会把 `tools/ios` 打进包
- 重启后看护对 236/237 走的是 `ios runwda`，不再是 xcodebuild；两台 `:8100` 均为 200

USB 还插着时 `kill -9 ios` 会拆掉机上 WDA（testmanagerd 还能收到会话结束）。拔线时主机发不出 stop，需要用户拔 236 再测 `:8100`。

## 2026-08-22 22:04 缺的配置：EnableWifiConnections

两台机 `SupportsWifi` / `SupportsWifiSyncing` 都是 true，但 lockdown 域 `com.apple.mobile.wireless_lockdown` 里没有 `EnableWifiConnections`。这就是 Xcode「Connect via network」/ Finder「在无线局域网上显示此 iPhone」。没打开时，调试会话只走 USB，拔线 DTServiceHub/testmanagerd 必拆，WDA 必停。

已对 236/237 写入 `EnableWifiConnections=true`。激活路径会调 `wifi-lockdown <udid>`，避免新机再漏。

## 对照 Appium WebDriverAgent v16.1.5

源码：https://github.com/appium/WebDriverAgent/tree/v16.1.5

- `FBWebServer.m` 的 `startHTTPServer` 与本仓库 WhatsAppDeviceAgent **无 diff**。`bindingIP` 为空则不 `setInterface`，听全部网卡；日志打印 `ServerURLHere->http://<ios.ip>:8100`。
- 机上 `/status` 已自报 `16.1.5` / `Built at Aug 19 2026 11:47:02`。重装同一份 IPA 不会换成另一套协议。
- 我们相对上游只在 `testRunner` 里多了注册页 UI，HTTP 绑定没改。

Appium XCUITest 官方 [Run Preinstalled WDA](https://github.com/appium/appium-xcuitest-driver/blob/master/docs/guides/run-preinstalled-wda.md)：

- **不跑 xcodebuild、只拉已装 Runner**：WDA v13+ **仅 iOS/tvOS 17+**。
- **iOS 15**（本机 7 Plus / Plus-2 是 15.8.8）：只能 `xcodebuild`，或对**已经在跑**的 WDA 设 `appium:webDriverAgentUrl`。
- 真机还要 **Developer Disk Image 已挂载**。iOS 15 的 DDI 随 USB 调试会话走，拔线会话掉则 Runner 掉。

所以官方「无线」是：Runner 还活着时用 `http://手机Wi-Fi:8100`。不是「拔掉 USB 后 iOS 15 的 XCTest 自己续命」。iOS 15 要拔线后会话还在，必须让 Xcode/usbmux **Connect via network** 接住调试通道。

## 对照 alibaba/Macaca

入口仓库 [alibaba/macaca](https://github.com/alibaba/macaca) 只是索引，iOS 实现在 [macacajs/macaca-ios](https://github.com/macacajs/macaca-ios) + [macacajs/XCTestWD](https://github.com/macacajs/XCTestWD)。

- 拉起：`xcodebuild test` / `test-without-building`（和我们曾经的 Mac 回退一样）。
- 真机：再起 `iproxy <port>:<port> -u <udid>`，客户端打 `127.0.0.1`。
- XCTestWD 日志写的是 `http://localhost:<port>`，不是「拔线后走手机 Wi-Fi」。
- `stop()` 会 `SIGKILL` xcodebuild 和 iproxy。

Macaca **没有**「激活后拔 USB、调试会话改走无线」的实现。它比 Appium WDA 更绑 USB。不能当拔线保活的对照实现来抄。

## 2026-08-22 22:24 无线 runwda 尝试

手机 Wi-Fi `192.168.10.236:62078`（lockdownd）在插着 USB 时是开的。`cmd/wifi-runwda` 把 go-ios 的 usbmux Connect 转到该地址。

- lockdown `:62078` 能连
- 已启动的服务端口也能 SYN
- `installation_proxy` 走 Wi-Fi 会 EOF
- `testmanagerd` DTX 走 Wi-Fi 会挂起，`:8100` 起不来

当前 236 已退回 USB `ios runwda`，`:8100` 恢复 200。拔线保活仍未闭环。

## 2026-08-22 22:50 打通的是 usbmux Network，不是直连 :62078

直连无线 lockdownd `:62078` 再 `StartService(testmanagerd.lockdown.secure)`：TCP/TLS 能握手，设备随即拆连接，DTX 起不来。iOS 15 的 DDI 服务要走主机 usbmux 的 **Network** 设备（Apple 无线调试通道）。Mac 是 `/var/run/usbmuxd`，Windows 是 `127.0.0.1:27015`，同一套。

`cmd/wifi-runwda` 改为：在真实 usbmux 里优先挑 `ConnectionType=Network` 的 DeviceID，再 exec `ios runwda`。

2026-08-22 22:45 对 Plus-2（`4886579a…` / `192.168.10.237`）实跑：

- usbmux：id=12 Network + id=9 USB
- `wifi-runwda` 选用 id=12
- 全部 Connect（lockdown / testmanagerd / installation_proxy / instruments）走 Network
- 约 8s 后 `:8100 /status` 200，`Process started pid=15412`

7 Plus（`59524996…` / `192.168.10.236`）当时只有 USB id=13，没有 Network。Bonjour `_apple-mobdev2` 在广播，`:62078` 开着，但 usbmuxd 未挂上无线条目。该机若现在拔线，仍会拆 USB DTX。

激活路径：有 `wifi-runwda` 就走它（有 Network 用 Network，没有则 USB）。`build-dmg.sh` 会把 `tools/wifi-runwda` 打进包。

## 2026-08-23 双通道规则（按产品要求收口）

- 拔 USB：只拆 `iproxy` 隧道，**不** `Stop` 机上 WDA；探活/发消息回退 `http://手机Wi-Fi:8100`。
- 存活判定：机上 `/status`（或主机激活进程仍在），**不以 USB 在线为前提**；40 位 UDID 与新机型同一套。
- 重拉起：USB **或** Wi-Fi 可达即可尝试（已去掉「老机型无 USB 必跳过」）。
- 激活：优先 `wifi-lockdown` + `wifi-runwda`（默认等待 usbmux `Network` 最多 45s），再 `ios runwda`；无 Network 时回退 USB 并打 WARN。

## 2026-08-23 00:42 审计补丁（群发死隧道窗口）

看护 `checkWDA` 已隧道→Wi-Fi 回退，但群发曾只走 `wdaBaseURLFor`：拔线后死隧道短暂残留会拒单。

已补：`resolveWDABaseURL` + `deviceReachable`/`NewClient` 对齐；`TunnelAddr` 查表前 `normalizeUDID`。

注意：App 包内 `wifi-runwda` 若仍是旧包则无 `-wait-network`，需重打 DMG 或同步 `tools/wifi-runwda`。无 usbmux Network 时激活仍 USB 回退，拔线会拆 XCTest。
