# Windows 激活 iOS WDA

- 状态：已在本机实现命令构造与后端分发；Windows 真机激活未在本环境核验
- 日期：2026-08-22
- 来源：别人现场拍的 25 秒视频 + 本仓库现有 `xcodebuild` 激活链路

## 1. 视频里实际看到了什么

素材：`微信视频2026-08-22_091234_651.mp4`，25.07 秒，720×1280 竖屏手机拍屏，无可用本地语音转写。

可直接从画面读出的事实：

| 观察 | 内容 |
|---|---|
| 主机 | Windows 桌面（任务栏 `2026/5/12 星期二 20:38`、中文输入法、缩放 100%） |
| PC 软件 | 标题「软件群发」的 RPA 控制台 |
| 任务 | `RPA2054155772430004224`，文案 `hello`，完成于 `2026-05-12 19:04:35` |
| 手机 | 刘海屏 iPhone，USB 插在支架上 |
| 系统横幅 | `Automation Running` / `Hold both volume buttons to stop` |
| 调试浮层 | `[s/WA]#…`、`ChatBar_SendButton`、`点击发送按钮`、某号码 `发送成功` |
| 配套 App | 「WS龙虾」：设备识别码 `119 72`、WhatsApp 账号区、云备份入口 |
| 结果 | 聊天里打出并送出 `hello nice to see you`（蓝色双勾） |

视频**没有**拍到「点激活 / 编 WDA / 装 Runner」这一步。拍到的是：WDA/XCTest 会话已经在跑，Windows 控制台在发群发任务，手机 WhatsApp 被脚本驱动。

「WS龙虾」是对方装在手机上的业务配套 App（设备号、账号、云备份），不是系统 WDA Runner。浮层是他们自己的脚本调试层，叠在 XCTest 会话上面。

## 2. 这件事为什么看起来像「Windows 能激活 iOS WDA」

把两件事拆开，就不会再被视频误导：

```text
签包 / 首次安装 Runner     ≠     每次拉起 XCTest
必须有 Apple 开发者证书           Windows / Linux / Mac 都能做
通常在 Mac 上 build 一次          走 testmanagerd 私有协议
或用 p12 + profile 重签已有 ipa
```

对方 Windows 机做的是右边这件事。左边在更早的时候已经做完：Runner 已经在手机上，开发者模式已开，证书已信任，USB 已配对。

本仓库今天的激活路径还停在左边+右边绑死 Xcode：

- `WDAManager.Activate` → `xcodebuild test-without-building`
- USB 发现已经走 `idevice_id -l`（跨平台），`ioreg` / `devicectl` 只是 Darwin 回退
- DDI 补齐读的是 Xcode `DeviceSupport`，Darwin 专属

所以「Windows 不能激活」不是 usbmux 做不到，是我们把「拉起 XCTest」写成了 `xcodebuild`。

## 3. 正确的 Windows 链路

```text
[一次，Mac 或带 p12 的任意机器]
  签好 WebDriverAgentRunner-Runner.app / .ipa
  装到手机（tidevice/go-ios install 或 Xcode 首次装）
  手机：开发者模式 + 信任开发者 + 信任此电脑

[每次，Windows 网关]
  Apple Mobile Device Support（iTunes / Apple Devices）提供 usbmux
  go-ios `ios runwda` 或 tidevice `xctest` 经 testmanagerd 拉起已安装 Runner
  iproxy / go-ios forward：本机端口 → 手机 8100
  网关原有 WDA HTTP 客户端照旧发会话 / 深链 / 点击
```

公开工具（都声明支持 Windows 拉起已安装的 WDA）：

- [go-ios](https://github.com/danielpaulus/go-ios) `ios runwda --bundleid=… --env=USE_PORT=8100`
- [tidevice](https://github.com/alibaba/tidevice) `tidevice -u <udid> xctest -B <bundle>`
- Appium XCUITest 的 [Run Preinstalled WDA](https://appium.github.io/appium-xcuitest-driver/latest/guides/run-preinstalled-wda/)

本工程 WDA 的 `PRODUCT_BUNDLE_IDENTIFIER` 是 `com.wda.WebRunner`，装到真机上的 UI test runner 一般是 `com.wda.WebRunner.xctrunner`。

iOS 版本差异：

- iOS ≤16：还要挂匹配的 Developer Disk Image。Mac 上本仓库已自动从 Xcode 拷；Windows 上要用 go-ios `image` 或预先挂好，不能再读 Xcode 目录。
- iOS 17+：不再走旧 DDI，要 RemoteXPC 隧道。go-ios 需要 Windows 上的 `wintun.dll`。

## 4. 本仓库的最小改动

不引入 go-ios 库依赖（避免未经授权拉模块），沿用现有「exec 子进程」风格：

| 配置 | 行为 |
|---|---|
| `signing.activator=auto`（默认） | Windows → `goios`，其它 → `xcodebuild` |
| `goios` | `ios --udid=… runwda --bundleid=com.wda.WebRunner.xctrunner …` |
| `tidevice` | `tidevice -u … xctest -B …`（不用 `wdaproxy`，避免和现有 iproxy 双转发） |
| `xcodebuild` | 原路径，不变 |

`signing.wda_bundle_id` 可覆盖默认 bundle id。

Mac 默认行为不变。Windows 上 `EnsureDeviceSupportDDI` 直接跳过；`ping` 改用 `-n 1 -w 2000`。

## 5. Windows 主机还缺什么（未在本机交付）

代码只解决「激活命令怎么拼、进程怎么看护」。要把一台 Windows 变成可运营的机房主机，还要：

1. 安装 Apple Devices / iTunes，确认 USB 枚举（`idevice_id -l` 有 UDID）。
2. 把 `ios.exe`（或 `tidevice.exe`）和 Windows 版 `iproxy`/`idevice_id`/`ideviceinfo` 放到 `PATH` 或 `WDA_GATEWAY_RESOURCES/bin`。
3. iOS 17+ 把 `wintun.dll` 放到系统目录，并拉起 go-ios tunnel。
4. 手机上已经装着本仓库签过的 Runner，且信任了同一张开发者证书。
5. 交叉编译网关：`GOOS=windows GOARCH=amd64 go build -o gateway.exe ./cmd/gateway`。

没有以上前置，只改 Go 代码无法在本机「演示 Windows 激活成功」。

## 6. 验收

- [x] `resolveActivator`：auto 在非 Windows 仍是 xcodebuild；显式 goios/tidevice 生效
- [x] `goiosArgs` / `tideviceArgs` 含 UDID、bundle、USE_PORT、WDA_DEVICE_UDID
- [x] 缺 `ios`/`tidevice` 二进制时 Activate 返回明确错误，不假装成功
- [ ] Windows 真机：`POST /api/devices/{udid}/activate` 后 `/status` ready
- [ ] 原 Mac `xcodebuild` 激活回归（默认路径未改命令）

回滚：把 `signing.activator` 设回 `xcodebuild`，或不配该项（Mac 默认原路径）。
