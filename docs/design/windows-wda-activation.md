# Windows 激活 iOS WDA

- 状态：命令构造与后端分发已实现；Mac 发送/连发已核验；Windows 真机激活未核验
- 日期：2026-08-22
- 全流程图：[wda-end-to-end-flow.md](./wda-end-to-end-flow.md)
- Mac 出包（独立章节）：[mac-wda-ipa-package.md](./mac-wda-ipa-package.md)
- 当晚操作步骤：[windows-night-runbook.md](../deployment/windows-night-runbook.md)
- 来源：竞品 25 秒视频 + 下列公开笔记
  - https://github.com/hiyongz/DevTest-Notes/blob/main/docs/app/app-testing-for-ios-app-on-windows.md
  - https://testerhome.com/topics/29230
  - https://hiyongz.github.io/posts/app-testing-for-ios-app-testing-on-windows-with-airtest/

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

本仓库日常激活按 **IPA** 走，不再绑死 Xcode：

- Mac 上 `scripts/package-wda-ipa.sh` 打出已签名 `wda.ipa`
- Windows / Mac 网关：手机还没装 Runner 就 `ios install --path=` / `tidevice install`，再 `ios runwda` 或 `tidevice xctest`
- `signing.activator=auto`：有 `ios` 用 goios，否则有 `tidevice` 用 tidevice；都没有时 Windows 仍报 goios，Mac 才回退 `xcodebuild`
- USB / Network 发现走 `idevice_id -l -n`（跨平台），`ioreg` / `devicectl` 只是 Darwin 回退
- 首次授权、USB/Network 互斥激活与 Mac **同一套业务规则**，见 [usb-network-activate.md](./usb-network-activate.md)
- DDI 补齐只在 Darwin 读 Xcode `DeviceSupport`；Windows 直接跳过

所以「Windows 不能激活」不是 usbmux 做不到，是 **必须先有一份签好名的 IPA**。装好之后，Windows 只负责右边。

## 3. 正确的 Windows 链路

```text
[一次，Mac]
  sh scripts/package-wda-ipa.sh  →  dist/wda.ipa
  拷到网关 -state 目录（或 -ipa 指定）

[每次，Windows / Mac 网关]
  Apple Mobile Device Support（iTunes / Apple Devices）提供 usbmux
  点激活：未装则 install IPA，再 ios runwda / tidevice xctest
  手机：开发者模式 + 信任开发者 + 信任此电脑
  iproxy / go-ios forward：本机端口 → 手机 8100
  网关原有 WDA HTTP 客户端照旧发会话 / 深链 / 点击
```

公开工具（都声明支持 Windows 拉起已安装的 WDA）：

- [go-ios](https://github.com/danielpaulus/go-ios) `ios runwda --bundleid=… --env=USE_PORT=8100`
- [tidevice](https://github.com/alibaba/tidevice) `tidevice -u <udid> xctest -B <bundle>`
- Appium XCUITest 的 [Run Preinstalled WDA](https://appium.github.io/appium-xcuitest-driver/latest/guides/run-preinstalled-wda/)

本工程 WDA 的 `PRODUCT_BUNDLE_IDENTIFIER` 是 `com.wda.WebRunner`，装到真机上的 UI test runner 一般是 `com.wda.WebRunner.xctrunner`。

iOS 版本差异（网关激活时自动分流，iOS 15 → 当前最新正式版）：

- iOS 15–16：挂匹配的 Developer Disk Image。Mac 从 Xcode DeviceSupport 拷；Windows/Linux 走 `ios image auto --basedir=<state>/ddi`。
- iOS 16+：激活前读 `ios devmode get`；未开开发者模式直接失败并提示去设置里打开。
- iOS 17+：不再走旧 DDI。激活前拉起并看护 `ios tunnel start --userspace`（默认绑 127.0.0.1:28100），`tunnel ls` 见到该机再 `install` / `runwda`。iOS 17+ 强制 go-ios，不用 wifi-runwda，也不用 tidevice。
- 用户态隧道覆盖 iOS 17.4+（含 18 / 26）。**不必**先装 `wintun.dll`。iOS 17.0–17.3 请升级；若必须留在 17.0–17.3，才需要管理员 + 内核隧道 + Windows `wintun.dll`。

## 4. 本仓库的最小改动

不引入 go-ios 库依赖（避免未经授权拉模块），沿用现有「exec 子进程」风格：

| 配置 | 行为 |
|---|---|
| `signing.activator=auto`（默认） | 有 `ios` → `goios`；否则有 `tidevice` → `tidevice`；都没有时 Windows 仍选 `goios`，Mac 回退 `xcodebuild` |
| `goios` | 未装 Runner 则 `ios install --path=<ipa>`，再 `ios --udid=… runwda --bundleid=com.wda.WebRunner.xctrunner …` |
| `tidevice` | 未装则 `tidevice -u … install <ipa>`，再 `tidevice -u … xctest -B …`（不用 `wdaproxy`） |
| `xcodebuild` | 原路径，仅作 Mac 回退 |

`signing.wda_bundle_id` 可覆盖默认 bundle id。  
`signing.ipa_path` 或启动参数 `-ipa` 指定 IPA；默认 `<state>/wda.ipa`。也可放在 `WDA_GATEWAY_RESOURCES/wda.ipa`。

Windows 上 `EnsureDeviceSupportDDI` 直接跳过；`ping` 改用 `-n 1 -w 2000`。

## 5. Windows 主机还缺什么（未在本机交付）

代码只解决「激活命令怎么拼、进程怎么看护」。要把一台日常 Windows 电脑跑起来，还要：

1. 安装 Apple Devices / iTunes，确认 USB 枚举（`idevice_id -l` 有 UDID）。
2. 把 `ios.exe`（或 `tidevice.exe`）和 Windows 版 `iproxy`/`idevice_id`/`ideviceinfo` 放到 `PATH` 或 `WDA_GATEWAY_RESOURCES/bin`。
3. iOS 17.4+ 由网关自动 `ios tunnel start --userspace`，一般不用 wintun。只有卡在 17.0–17.3 且要走内核隧道时，才把 `wintun.dll` 放到系统目录并管理员启动。
4. 把 `wda.ipa` 放到网关状态目录（或 `-ipa`），手机信任同一张开发者证书。
5. 交叉编译网关：`GOOS=windows GOARCH=amd64 go build -o gateway.exe ./cmd/gateway`。

没有以上前置，只改 Go 代码无法在本机「演示 Windows 激活成功」。

## 6. 验收

- [x] `resolveActivator`：auto 优先 goios/tidevice；显式值生效
- [x] `goiosArgs` / `tideviceArgs` 含 UDID、bundle、USE_PORT、WDA_DEVICE_UDID
- [x] `goiosInstallArgs` / `tideviceInstallArgs` 与 applist 参数
- [x] 缺 `ios`/`tidevice` 二进制时 Activate 返回明确错误，不假装成功
- [x] iOS 版本分流：15–16 DDI / 16+ 开发者模式 / 17+ 用户态隧道 + 强制 go-ios
- [x] `ios tunnel start --userspace` 守护、复用已有 agent、网关退出停本进程拉起的守护
- [x] iOS 17+ `install`/`apps`/`runwda` 带 `--tunnel-info-port=28100`；不走 wifi-runwda
- [ ] Windows 真机：放好 `wda.ipa` 后 `POST /api/devices/{udid}/activate`，未装会 install，然后 `/status` ready
- [ ] iOS 17.4+ 真机：点激活后管理页出现「iOS17+ 隧道」且能发出一条消息

回滚：把 `signing.activator` 设为 `xcodebuild`（仅 Mac）。
