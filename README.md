# WDA Farm Gateway（本地网关）

> 独立 Go 项目，与手机应用（WebDriverAgent / agent）代码无关，仅通过 xcodebuild 构建依赖
> 引用的 WDA 工程（默认同级 `WhatsAppDeviceAgent/`，可用 `-project` 覆盖）。

Mac mini 上常驻的本地网关：发现 USB 真机、按 UDID 激活/看护 WDA、Web 页面管理、
通过 WSS 向云平台上报设备列表并接收任务（三端串联 v6：手机零直连，全部由网关中转）。

## 架构

```
云平台 ◀── WSS（网关凭证登录 / device_list / task:dispatch / item:result）── 本地网关（本应用）
                                              │ USB 扩展坞
                                              ▼
                                    iPhone 1..N（各 Wi-Fi IP:8100）
```

## 机型/系统兼容

- **机型**：iPhone 7 及以上（A10 及以后；构建产物为 arm64，兼容 arm64/arm64e 设备）。
- **系统**：iOS 15 及以上。
  - iOS 16+ 需在设备「设置 → 隐私与安全性」开启「开发者模式」；
  - iOS 15/16 老设备激活时网关自动从本机 Xcode 复制匹配版本的 DeveloperDiskImage
    到设备支持目录（`~/Library/Developer/Xcode/iOS DeviceSupport/`，幂等）；
    若本机 Xcode 不含 iOS 15 镜像（建议 Xcode 16.x），请手动放置对应 DDI。
- **识别/激活**：老机型（40 位 UDID，iOS ≤15）用无前缀 `id=` destination，
  新款（连字符 UDID，iOS 16+）用 `platform=iOS,id=`+大写，网关自动区分。

## 快速开始

```bash
go build -o gateway ./cmd/gateway
./gateway -config devices.json -listen 0.0.0.0:8300
# Web 页面:  http://localhost:8300/
# API:       http://localhost:8300/api/devices  |  /api/cloud
```

- `-project`：WhatsAppDeviceAgent 工程路径（默认 `../whatsapp_ai_ios/WhatsAppDeviceAgent`）。
- `-derived`：xcodebuild derivedDataPath（默认 `/tmp/WebDriverAgentFarmDerived`）。

## 配置（devices.json）

```json
{
  "cloud": {
    "ws_url": "wss://<平台域名>/api/ios-agent/v1/gateway/ws",
    "token": "<平台签发的网关凭证>",
    "gateway_name": "macmini-01",
    "enabled": true
  },
  "health_interval": 30,
  "llm": {
    "base_url": "https://api.example.com/v1",
    "api_key": "<密钥>",
    "model": "<视觉模型名>"
  },
  "devices": [
    { "udid": "40位UDID", "ip": "手机Wi-FiIP", "port": 8100, "auto_reactivate": true }
  ]
}
```

- 网关凭证在平台「组网」页签发，凭证明文仅展示一次，请保存到 `cloud.token`。
- `llm` 为可选视觉兜底：配置后，发送键选择器找不到时用截图 + 视觉模型定位坐标点击；不配置则保持默认选择器发送。
- 设备列表由 USB 与 Wi-Fi 实时发现：USB 直连（ioreg/devicectl）或 Wi-Fi 上 WDA 健康可达才在线，
  不在线就不出现在列表里；插上 USB 即可直接「激活」，手机 Wi-Fi IP 由网关局域网扫描自动发现，无需手动设置。

## 云通道协议（三端串联 v6，JSON 文本帧）

上行（`Authorization: Bearer <gateway_token>`）：
```json
{"v":1,"type":"gateway:hello","msgId":"g:1","sentAt":"...","payload":{"name":"macmini-01","version":"0.2.0"}}
{"v":1,"type":"gateway:heartbeat","msgId":"g:2","sentAt":"...","payload":{}}
{"v":1,"type":"device_list","msgId":"g:3","sentAt":"...","payload":{"devices":[
  {"udid":"5060c403...","name":"iPhone","model":"iPhone9,2","ios_version":"15.8.7",
   "wda_ip":"192.168.20.33","wda_port":8100,"wda_status":"online","whatsapp_version":""}]}}
{"v":1,"type":"item:result","msgId":"g:4","sentAt":"...","payload":{"task_id":"t1","item_id":"i1","phone":"+8613800000000","status":"sent","error":"","duration_ms":3200}}
{"v":1,"type":"device:status","msgId":"g:5","sentAt":"...","payload":{"udid":"5060c403...","wda_status":"online","error":""}}
```
下行：
```json
{"v":1,"type":"task:dispatch","msgId":"srv:1","sentAt":"...","payload":{"task_id":"t1","device_id":"d1","udid":"5060c403...","content":"你好","interval_sec":20,"items":[{"item_id":"i1","phone":"+8613800000000","seq":1}]}}
{"v":1,"type":"task:cancel","msgId":"srv:2","sentAt":"...","payload":{"task_id":"t1"}}
```

- 网关收到 `task:dispatch` 后按 UDID 串行执行：WDA 建会话 → `whatsapp://send?phone=` 深链 →
  输入内容 → 点发送；每条 item **先本地持久化再上报**（`data/results/<task_id>.json`，at-least-once）。
- 重连后先补报本地已持久化结果，再上报 device_list；平台随后补推 pending 任务。
- 收到 `task:cancel` 停止循环，未执行 items 标 cancelled 上报。
- 平台「组网」页吊销凭证后本网关会被踢出（关闭码 4005）且重连被拒。
- 断线自愈：会话断开后按指数退避重连（1s→30s，封顶），稳定会话（>60s）断开则直接快速重连；
  凭证被吊销（4005）时改为 60s 慢速重试。
- 常见告警「failed to read JSON message: … frame header: EOF」表示平台侧 TCP 直接断开（未发关闭帧），
  通常是平台重启/部署或网络抖动，网关会自动重连，并非 JSON 解析错误；每 20s 心跳含协议层
  ping + 应用层 `gateway:heartbeat`，同时有写超时与会话级上下文兜底，不会卡死或向旧连接写数据。
- 拨号告警「…handshake response status code 101 but got 502/503/504」表示连接握手时平台暂不可用
  （正在重启/部署），401/403 表示凭证无效或被吊销；两类都会自动重试（后者慢速），无需干预，
  平台恢复后网关自行重连。

## REST API

| 方法 | 路径 | 说明 |
|---|---|---|
| GET  | /api/cloud | 云通道状态（连接/网关名/执行中任务） |
| GET  | /api/devices | 设备列表（USB/Wi-Fi 实时发现，含 WDA 进程/健康/忙碌状态） |
| POST | /api/devices/{udid}/activate?port=8100 | 激活该设备 WDA（USB 直连即可） |
| POST | /api/devices/{udid}/stop | 停止该设备 WDA |
| GET  | /api/devices/{udid}/health | 健康检查（需已发现 IP） |
| POST | /api/devices/{udid}/report | 本地/调试结果上报（旧协议，兼容保留） |
| GET  | /api/devices/{udid}/metrics | 本地发送指标 |

## 说明

- `wda.py` 首次激活前会 `build-for-testing` 一次（产物放 `derived_data`），之后每台只跑
  `test-without-building`，按 UDID 后台进程独立管理；`send_message` 为 WhatsApp 发送流程
  （选择器与平台 `internal/wda/whatsapp.go` 对齐，真机联调时按需覆盖）。
- `watchdog.py` 每 `health_interval` 秒逐台健康检查，WDA 挂了且未在激活则自动重新激活；
  健康状态变化通过 `device:status` 上报平台。
- 激活用 `USE_PORT` 指定端口（默认 8100）；多台走各自 IP 时无需改端口。
- easytier 为可选后备能力、默认关闭：仅当平台下发 `easytier:config`（v1 默认不启用）时按需集成。

## 新 Mac 一键安装

```bash
sudo sh scripts/install.sh   # 安装 easytier 二进制 + 权限组授权（sudoers），首次执行输一次密码
cd gateway && ./run.sh       # 启动网关（Web http://localhost:8300）
```

`scripts/install.sh` 幂等，做四件事：
1. 安装 `tools/easytier/easytier-core|cli`（v2.6.4 macOS aarch64，缺省自动下载，记录 sha256）
2. 创建权限组 `wda-gateway` 并把当前用户加入组
3. 写 sudoers：`%wda-gateway` 组规则（长期管理）+ 当前用户规则（当前会话立即生效，
   规避 macOS 组缓存需重登的问题）；新增网关用户只需
   `sudo dscl . -append /Groups/wda-gateway GroupMembership <用户名>`
4. 验证免密：`sudo -n easytier-core --version`

授权（开启虚拟网卡 TUN 所需 root）仅在首次安装时配置一次，之后 Web 启动 / 平台下发
easytier:config 触发的重启均 `sudo -n` 免密，不再弹窗。

## easytier 后备通道（可选，默认关闭）

平台「组网」页可向在线网关下发 `easytier:config`（WSS 下行）；网关收到后保存配置并启动本机
集成的 easytier 服务加入 mesh，作为 WSS 之外的后备链路（v6 §5.1/§7.2）。

- 二进制（项目内，不入库）：下载 easytier v2.6.4 macOS aarch64 到 `tools/easytier/`：
  ```bash
  curl -fsSL -o /tmp/et.zip https://github.com/EasyTier/EasyTier/releases/download/v2.6.4/easytier-macos-aarch64-v2.6.4.zip
  mkdir -p tools/easytier
  python3 -c "import zipfile; zipfile.ZipFile('/tmp/et.zip').extractall('/tmp/et')"
  install -m 0700 /tmp/et/easytier-macos-aarch64/easytier-core tools/easytier/easytier-core
  install -m 0700 /tmp/et/easytier-macos-aarch64/easytier-cli tools/easytier/easytier-cli
  ```
- 查询复用 easytier RPC 门户（`easytier-cli -p 127.0.0.1:15888`），与平台 `internal/easytierrpc` 同源语义。
- **虚 IP 由平台动态下发**：平台「组网」页下发 easytier:config 时为网关自动分配虚 IP（10.168.1.0/24，
  固定复用），网关据此以 root 运行 easytier-core（bind_device=true 建 TUN），节点在 mesh 内
  拥有完整虚 IP，peer 列表本机行完整显示（hostname/ipv4/cost/lat/nat/version）。
- **root 一次性授权**：macOS 上 ipv4 非空即创建 TUN，需要 root；执行一次
  `sudo sh scripts/setup-easytier-sudo.sh`（首次输密码），为当前用户放行免密运行 easytier-core
  与 `pkill -f easytier-core*`（最小权限，仅这两个命令）。
- 本地 REST：`GET /api/easytier/status`（运行/节点/peers）、`GET /api/easytier/config`（脱敏）、
  `PUT /api/easytier/config`（编辑）、`POST /api/easytier/action {start|stop|restart}`。
- Web 页底部「组网（easytier 后备通道）」区块可查看配置/状态/peer 并启停。
- 默认无 TUN 模式（`ipv4=""`，普通用户可运行，纯转发节点）；配置 `tun=true` 且填 `gateway_ipv4`
  时以 TUN 模式运行并绑定虚 IP（需 root 权限）。
