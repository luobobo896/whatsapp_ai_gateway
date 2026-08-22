# Windows 当晚接入手册

给今晚回家插上 Windows 电脑用。Mac 上发送已经跑通；Windows 要验收的是 **在 Windows 上把手机 WDA 拉起来，再走同一套发送**。

- 状态：Mac 发送已核验；Windows 真机激活未核验
- 日期：2026-08-22
- 用户机：Windows（主路径）。本机构目前没有 Windows 主机

## 0. 先记住这张图

```text
[一次，需要 Mac]
  Xcode 把 WebDriverAgentRunner 装到 iPhone 并信任证书

[每次，Windows 就能做]
  Apple Devices / iTunes 提供 usbmux
  tidevice xctest 或 ios.exe runwda 拉起已安装的 Runner
  iproxy 把本机端口转到手机 8100
  gateway.exe 打开会话 → 输入 → 发送 → 回传
```

不要接 Appium、Airtest、`tidevice wdaproxy` 当产品路径。文章里那些是客户端或会和 `iproxy` 抢端口。

依据：

- [tidevice + facebook-wda](https://github.com/hiyongz/DevTest-Notes/blob/main/docs/app/app-testing-for-ios-app-on-windows.md)
- [Win 下 Appium + iOS](https://testerhome.com/topics/29230)（先 tidevice 再把 `webDriverAgentUrl` 指到本机）
- [tidevice + Airtest](https://hiyongz.github.io/posts/app-testing-for-ios-app-testing-on-windows-with-airtest/)
- 设计说明：[windows-wda-activation.md](../design/windows-wda-activation.md)
- 全流程图：[wda-end-to-end-flow.md](../design/wda-end-to-end-flow.md)

## 1. 今晚出门前在 Mac 上确认

这些已经做过，回家不用重做发送逻辑：

| 项 | 结果 |
|---|---|
| USB iPhone 7 Plus / iOS 15.8.8 | `idevice_id -l` 看得到 |
| WDA `/status` | ready，USB 隧道 `127.0.0.1:18100` |
| 单条发送 | `hello nice to see you` 发出，输入框清空 |
| 连发 3 条 | 整单复用会话，约 6s/条，见 [smooth-batch-mac](../testing/2026-08-22-smooth-batch-mac.md) |
| `gateway.exe` | `sh scripts/build-windows-exe.sh` → `dist/windows-amd64/gateway.exe` |
| 手机上的 Runner | 本仓库 `com.wda.WebRunner.xctrunner`（Mac 上 `xcodebuild test-without-building` 已装过） |

回家带上：这台已经信任过开发者证书的 iPhone、数据线、`dist/windows-amd64/` 整个目录。

## 2. Windows 电脑要装的东西

按顺序，少一步后面全假成功。

1. **Apple Devices** 或 **iTunes**（提供 usbmux）。装完重启一次再插手机。
2. 手机：开发者模式开着、已信任此电脑、设置里已信任开发者 App。
3. Python 3.6+ 后：`pip3 install -U "tidevice[openssl]"`，或下载 [go-ios](https://github.com/danielpaulus/go-ios) 的 `ios.exe`。
4. Windows 版 `idevice_id` / `iproxy`（libimobiledevice）。放到 `PATH`，或放到网关旁边的 `bin\` 并设 `WDA_GATEWAY_RESOURCES` 指向该目录的上一级。
5. iOS 17+ 才需要 `wintun.dll`。今晚这台 iPhone 7 是 **iOS 15.8.8，不需要**。
6. 把 `dist/windows-amd64/gateway.exe` 和 `static\` 拷到 Windows。不要拷 `devices.json` / `gateway.db` / 云凭证。

网关默认：Windows 上 `signing.activator=auto` → `ios.exe runwda`。  
没有 `ios.exe`、只有 tidevice 时，在管理页或配置里把激活后端改成 `tidevice`。

## 3. 插上手机后的核验顺序

不要跳。文章里失败最多的是第 4 步去连手机 Wi‑Fi IP。

```bat
tidevice list
tidevice applist
```

`list` 必须打出 UDID。`applist` 里必须有 `com.wda.WebRunner.xctrunner`（或你改过的 bundle）。没有 Runner = 回家用 Mac 再装一次，Windows 编不了。

然后二选一拉起 WDA（不要用 `wdaproxy`）：

```bat
REM 推荐。tidevice 环境变量是 -e KEY:VALUE（冒号），不要写成 --env USE_PORT=8100
tidevice -u <UDID> xctest -B com.wda.WebRunner.xctrunner -e USE_PORT:8100 -e WDA_DEVICE_UDID:<UDID>

REM 端口默认就是 8100 时可以更短
tidevice -u <UDID> xctest -B com.wda.WebRunner.xctrunner

REM 或 go-ios（网关 Windows 默认，这里才是 KEY=VALUE）
ios --udid=<UDID> runwda --bundleid=com.wda.WebRunner.xctrunner --testrunnerbundleid=com.wda.WebRunner.xctrunner --xctestconfig=WebDriverAgentRunner.xctest --env=USE_PORT=8100
```

另开一个窗口做 USB 转发：

```bat
iproxy -u <UDID> 18100:8100
curl http://127.0.0.1:18100/status
```

必须看到 `"ready" : true`。  
手机顶上会出现 **Automation Running / Hold both volume buttons to stop**，和竞品视频同一条横幅。

**禁止**用 `http://192.168.x.x:8100/status` 当激活成功。锁屏后 Wi‑Fi 会掉。

## 4. 启动网关并发一条

```bat
gateway.exe
```

浏览器打开 `http://127.0.0.1:8300/`。USB 设备出现后点激活（若第 3 步已经手动拉起且 `/status` ready，网关探活即可）。

发一条（与 Mac 同一验收）：

```bat
wda-probe.exe -wda http://127.0.0.1:18100 -phone 15213472085 -text "hello nice to see you" -send -count 3 -interval 1
```

`wda-probe.exe` 若没交叉编，可在 Mac 上 `GOOS=windows GOARCH=amd64 go build -o dist/windows-amd64/wda-probe.exe ./cmd/wda-probe` 一并拷走。

成功标准（和竞品视频同一套）：

1. 打开目标聊天  
2. 输入框打出文案  
3. 点发送后气泡出现、输入框清空  
4. 回传 `status=sent`  
5. 第 2、3 条不应再出现十几秒的 CreateSession（热路径大约数秒级）

## 5. 常见失败（来自那三篇文章 + 本仓库）

| 现象 | 先查 |
|---|---|
| `tidevice list` 空 | 没装 Apple Devices/iTunes，或没点「信任此电脑」 |
| applist 没有 xctrunner | Runner 没装或证书过期，回 Mac 重装 |
| `/status` connection reset | WDA 没在跑，或 iproxy 指错 UDID |
| Appium / 脚本连 `192.168.x:8100` 超时 | 改连 `127.0.0.1:<iproxy口>` |
| `More than 2 devices` | 必须加 `-u <UDID>`，每台一个本地端口 |
| 激活报找不到 `ios` / `tidevice` | 二进制不在 PATH，也没放进 `WDA_GATEWAY_RESOURCES\bin` |
| 每条都 20s+ | 旧逻辑每条 CreateSession；确认跑的是今天改过的 gateway（整单复用会话） |

## 6. 不要做的事

- 不要在 Windows 上装 Xcode / 不要指望 Windows 从零编 WDA  
- 不要把 `tidevice wdaproxy` 和网关 `iproxy` 一起开  
- 不要接 Appium、Airtest 当发送层  
- 不要把云 token、`gateway.db`、`devices.json` 打进 U 盘包  
- 不要对非测试号群发验收

## 7. 当晚回传要记下的证据

请保留（脱敏后即可）：

- `tidevice list` / `applist` 各一截  
- `curl http://127.0.0.1:18100/status` 的 JSON  
- 三条发送的 probe 输出或 `report.json`  
- 手机截图：气泡 + 空输入框  

缺 Windows 主机时，**不能**把本节标成已验收。
