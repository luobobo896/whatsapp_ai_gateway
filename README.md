# WDA Farm Gateway（本地网关）

Go 写的本机网关：USB 发现 iPhone、拉起 WebDriverAgent、管理页、经 WSS 接云平台收群发任务。  
手机不直连平台，全部由本机中转。

**日常用的电脑主机是 Windows。** 苹果电脑只负责编译做出并签好签名的 WDA「控制器」安装包，见独立章节 [docs/design/mac-wda-ipa-package.md](docs/design/mac-wda-ipa-package.md)。整条链路通俗说明见 [docs/design/wda-end-to-end-flow.md](docs/design/wda-end-to-end-flow.md)。

## 架构

```
云平台  ◀── WSS（登录 / 设备列表 / 下发任务 / 回传结果）──  本机网关
                                                          │ USB（iproxy 隧道优先）
                                                          ▼
                                                    iPhone 上的 WDA :8100
                                                          │
                                                          ▼
                                                    WhatsApp 打开聊天 → 输入 → 发送
```

## 机型 / 系统

- iPhone 7 及以上，**iOS 15 到当前最新正式版**（含 16 / 17 / 18 / 26）。
- iOS 16+ 必须打开开发者模式。未开时激活会直接报错，不会假装成功。
- 老机型（40 位 UDID）激活用 `id=<udid>`；带连字符的新 UDID 用 `platform=iOS,id=` 且大写。
- 版本分流（点激活自动做，不要人手跑文章里的命令）：
  - **USB 与 Network 互斥**：点哪条通道就只走哪条，失败不回退。USB 走 `ios runwda` / xcodebuild / tidevice；Network（iOS 15–16）走 `wifi-runwda -require-network`。iOS 17+ 的 Network 激活会拒绝（不能改走 go-ios USB 隧道）。
  - **首次授权只走 USB**：插着 USB、手机已设锁屏密码后，点「首次授权」才写 `EnableWifiConnections` / `EnableWifiDebugging`。Network 激活不写这两个开关。未授权则 Network 不可用。
  - **发现**：`Discover()` 同时读 `idevice_id -l -n` 的 USB 与 Network，拔线后无线设备仍进列表。
  - **iOS 15–16**：Mac 补 Xcode 开发者镜像；Windows 仅在需要新装 IPA 时才 `ios image auto`。
  - **iOS 17+ USB**：先拉起 `ios tunnel start --userspace` 再 `runwda`。iOS 17.0–17.3 请升到 17.4+。
- 日常激活（Windows / Mac 相同）：把 Mac 签好的 `wda.ipa` 放到网关状态目录，点 USB 或 Network 激活。未装 Runner 会 `install`，再按所选通道拉起（不要 `wdaproxy`）。
- **Mac 与 Windows 业务规则相同**（首次授权、USB/Network 互斥、发现、停止）。当前约定见 [docs/design/usb-network-activate.md](docs/design/usb-network-activate.md)。
- **自主群发（Autonomy）业务两端一致**：自主回路、探活回退（USB 无隧道 → `ip:port`）、已发联系人去重（`sent_contacts`）、`/api/ws` 实时任务通知，Mac/Windows **业务行为一致**。实现层面按平台而定（如 Windows usbmux 套接字 / netmuxd、工具链、USB/Network 激活通道互斥），交付时按平台选用对应实现——与既有"Mac 与 Windows 业务规则相同"原则一致。详见 [docs/design/gateway-autonomous-agent.md](docs/design/gateway-autonomous-agent.md) 与 [docs/design/usb-network-activate.md](docs/design/usb-network-activate.md)。
- 只有 Mac 上没有 `ios`/`tidevice` 时才回退 `xcodebuild`。不要在缺 iOS Platform 时反复 `build-for-testing`（会报 exit 70）。

## 快速开始（按系统分开）

日常发消息用 **Windows**。苹果电脑只做编译和签名，不是每天开机发消息的那台。

仓库 `tools/` 已带 go-ios（`ios` / `ios.exe`）、`wifi-runwda`、`wifi-lockdown`、已签名 `wda.ipa`。在仓库根目录启动网关会自动找到，不必再单独下载。说明见 [tools/README.md](tools/README.md)。

**Windows（日常主机）**

装 Apple Devices 或 iTunes，插 iPhone，点信任。普通权限即可发消息；只有开 easytier 虚拟网卡时才右键 **以管理员身份运行**。

源码启动（本机已装 Go）：

```bat
go build -o gateway.exe .\cmd\gateway
gateway.exe -state . -listen 0.0.0.0:8300
```

也可以直接 `go run .\cmd\gateway -state . -listen 0.0.0.0:8300`。  
Windows 走 `ios.exe runwda` / `tidevice`，**不跑 xcodebuild**，不必准备 WDA 工程源码。  
把 Mac 打好的 `wda.ipa` 放到 `-state` 目录（或 `-ipa` 指定）。手机还没装控制器时，点激活会先安装再启动。

打包启动：
- **桌面壳（推荐）**：`dist/windows-amd64/WDAFarmGateway.exe` 双击 → 托盘 + WebView2 管理窗（不必手动开浏览器）。说明见 [docs/deployment/windows-desktop-shell.md](docs/deployment/windows-desktop-shell.md)。
- **仅网关**：`dist/windows-amd64/gateway.exe` 双击后浏览器打开 http://127.0.0.1:8300/。

构建：`sh scripts/build-windows-exe.sh`（Mac/Linux 交叉编译，含桌面壳）。  
当晚步骤：[docs/deployment/windows-night-runbook.md](docs/deployment/windows-night-runbook.md)。

**Mac（出包 / 开发）**

桌面端：打开 `WDAFarmGateway.app`。配置在  
`~/Library/Application Support/WDAFarmGateway/gateway.db`。

打控制器安装包（只需在有 Xcode 的苹果电脑上做）。个人账号要先登记目标手机 UDID；企业 In-House 用同一脚本设 `SIGN_MODE=enterprise`，不必登记设备。完整步骤见 [Mac 出包独立章节](docs/design/mac-wda-ipa-package.md)：

```bash
sh scripts/package-wda-ipa.sh
# 产出 dist/wda.ipa
```

日常启动网关（Mac 和 Windows 一样，装 IPA + 拉起，不打开 Xcode 工程）：

```bash
go build -o gateway ./cmd/gateway
./gateway -state . -listen 0.0.0.0:8300
# 或：./gateway -state . -ipa /path/to/wda.ipa
```

把 `dist/wda.ipa` 拷到 `-state` 目录并命名为 `wda.ipa`。本机有 `ios` 或 `tidevice` 时，`auto` 会走这条 IPA 路径。  
`-project` / `-derived` 只在没有协议工具、必须回退 `xcodebuild` 时才用。

常用参数：

| 参数 | 含义 |
|---|---|
| `-state` | 状态目录（`gateway.db`、`data/`、默认 `wda.ipa`）。默认当前目录或 `GATEWAY_STATE_DIR` |
| `-ipa` | 已签名 WDA IPA。默认 `<state>/wda.ipa` |
| `-project` | 仅 xcodebuild 回退：WDA 的 `.xcodeproj` 所在目录 |
| `-derived` | 仅 xcodebuild 回退：编译产物目录，默认 `<state>/derived` |
| `-listen` | HTTP 地址，默认 `0.0.0.0:8300` |
| `-config` | 旧参数，只取其**目录**当 `-state`，不再读写 `devices.json` |

## 配置存在哪

全部在 `<state>/gateway.db`（SQLite）：云地址、凭证、设备、LLM、easytier。首次启动自动建库；管理页登录成功后写入凭证。

不要再使用仓库根的 `devices.json`（含密钥，已被 gitignore）。若某目录里还有这份旧文件且该目录的库是空的，启动会导入一次并改名为 `devices.json.bak`。

LLM 是可选视觉兜底：选择器找不到发送键时截图定位。欠费/401 冷却 10 分钟，不挡主路径。

## 发送

收到 `task:dispatch` 后按 UDID 串行：

1. 整单共用一条 WDA 会话（不要每条都 CreateSession）
2. 打开目标聊天（已在该会话则直接发）
3. 输入 → 点发送 → 确认输入框已清空
4. 先写入 `data/results/results.db`，再 `item:result` 上报（至少一次）

探针（WDA 已 ready）：

```bash
go run ./cmd/wda-probe -wda http://127.0.0.1:18100 \
  -phone 15213472085 -text 'hello nice to see you' -send -count 3 -interval 1
```

选择器与禁令：[docs/testing/whatsapp-send-playbook.md](docs/testing/whatsapp-send-playbook.md)。

## REST API

管理页接口需登录（云通道已配置时）。公开：`/api/login`、`/api/session`、`/api/cloud/config`。

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | /api/login | 登录（成功可签发网关凭证） |
| GET  | /api/session | 登录状态 |
| GET  | /api/cloud | 云通道状态 |
| GET/PUT | /api/cloud/config | 云地址等（脱敏读） |
| GET  | /api/devices | USB/Wi‑Fi 发现的设备（含 `ios_version` / `needs_tunnel` / `tunnel_ready`） |
| POST | /api/devices/{udid}/activate | 拉起该机 WDA |
| POST | /api/devices/{udid}/stop | 停止该机 WDA |
| GET  | /api/devices/{udid}/health | 健康检查 |
| GET  | /api/metrics | 发送统计 |
| GET  | /api/tasks | 本地任务列表 |
| GET  | /api/llm | 模型配置（不回传明文 key） |
| GET/PUT | /api/easytier/config | easytier 脱敏配置 |
| GET  | /api/easytier/status | easytier 运行状态 |
| POST | /api/easytier/action | start / stop / restart |

看护按 `health_interval`（默认 30s）探活；WDA 掉了且允许自动激活则重拉。USB 隧道优先于手机 Wi‑Fi。

## 云通道（摘要）

上行：`gateway:hello` / `heartbeat` / `device_list` / `item:result` / `device:status` / `task:summary`。  
下行：`task:dispatch` / `task:cancel`。凭证放请求头 `Authorization: Bearer <token>`。

断线按指数退避重连。关闭码 4005 表示平台吊销凭证。握手 502/503/504 是平台暂不可用，会自动重试。

## easytier（可选，默认关）

只有平台下发 `easytier:config`、要开虚拟网卡时才需要。平时发 WhatsApp **不用开**。

| | Mac | Windows |
|---|---|---|
| 做什么 | 首次 `sudo sh scripts/install.sh`：装 easytier 二进制，并配好免密 sudo | **没有**这套 install.sh。需要组网时，**右键以管理员身份运行** `gateway.exe` |
| 做几次 | 一台 Mac 做一次，以后管理页点启动不再输密码 | 每次要开虚拟网卡，用管理员启动网关即可 |
| 不做会怎样 | 开 TUN 会要管理员授权或失败 | 普通权限启动时，开 TUN 会失败；不影响只发消息 |

不要把云 token 打进安装包。Windows 版 easytier 二进制需另备，当前 exe 打包不附带。

## 文档

| 文档 | 给谁看 |
|---|---|
| [docs/design/mac-wda-ipa-package.md](docs/design/mac-wda-ipa-package.md) | 独立章节：苹果电脑编出并签好 `wda.ipa` |
| [docs/design/wda-end-to-end-flow.md](docs/design/wda-end-to-end-flow.md) | 不懂技术：编译签名 → 安装启动 → 自动发送 |
| [docs/deployment/windows-night-runbook.md](docs/deployment/windows-night-runbook.md) | Windows 当晚逐步命令 |
| [docs/design/windows-wda-activation.md](docs/design/windows-wda-activation.md) | 激活后端与 tidevice 参数 |
| [docs/deployment/windows-exe打包.md](docs/deployment/windows-exe打包.md) | 交叉编译 exe |
| [docs/deployment/macos-dmg打包操作手册.md](docs/deployment/macos-dmg打包操作手册.md) | Mac 桌面端打包 |
| [docs/deployment/macos-dmg替换操作手册.md](docs/deployment/macos-dmg替换操作手册.md) | Mac 用新包替换 `/Applications` 已装应用（停→建→换→启，含备份/回滚） |
