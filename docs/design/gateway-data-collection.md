# 网关数据收集设计（群发明细 / 任务汇总 / 设备上下文）

> 2026-08-14 · 范围：仅网关侧（whatsapp_ai_gateway）。云平台侧对接待后续指令另行实施。

## 1. 背景与目标

云平台群发任务下发到网关后，此前网关上行/落盘的单条结果只有 6 个字段
（task_id / item_id / phone / status / error / duration_ms），无法回答商业群发软件的基本问题：
**发给了谁、手机号多少、成功还是失败、发了什么内容、用哪台设备、USB 还是 Wi-Fi 连接、任务为什么结束**。

本次变更把网关侧数据收集补齐到商业软件应具备的水平；上行协议新增字段均为 `omitempty`，
平台旧版解析时忽略未知字段，向后兼容，为云平台侧升级预留好数据源。

## 2. 商业群发软件的数据全景（作为平台用户希望拿到的数据）

| 维度 | 字段 | 状态 |
|---|---|---|
| **单条明细** | 手机号、收件人姓名、实际发送内容、状态（成功/失败/取消）、失败原因、耗时、发送时间、是否新会话 | ✅ 本次补齐 |
| **设备上下文**（随明细带上） | UDID、硬件序列号、设备名称、连接方式（usb/wifi） | ✅ 本次补齐 |
| **任务级汇总** | 任务总数、成功/失败/取消/未发计数、开始/结束时间、总耗时、结束原因（完成/熔断/预热上限/设备失联/未配置 IP） | ✅ 本次新增 |
| **设备清单**（device_list） | UDID、序列号、名称、型号、iOS 版本、WDA 地址、连接方式、WDA 状态 | ✅ 连接方式本次补齐 |
| **每日统计**（metrics.json） | 分设备 sent_ok / sent_fail / total / new_sessions + 按天历史 | ✅ 已有 |
| 电池电量、WhatsApp 版本、WhatsApp 登录状态、重试次数 | — | ⏸ 未做（见 §6） |

## 3. 上行协议变更（网关 → 云平台）

### 3.1 `item:result`（单条明细，增强字段全部 omitempty）

```jsonc
{
  "task_id": "...", "item_id": "...",
  "phone": "8613800000001",
  "status": "sent | failed | cancelled",
  "error": "",
  "duration_ms": 8213,
  // ---- 以下为本次新增 ----
  "udid": "4886579a...",          // 发送设备 UDID
  "serial": "C38SG3S0HG00",       // 设备硬件序列号
  "device_name": "运营机-01",      // 设备名称
  "conn_type": "usb | wifi",      // 发送时刻连接方式
  "content": "13800000001 您好…",  // 实际发送内容（逐条渲染后）
  "contact_name": "张三",          // 收件人姓名（best-effort，读不到为空）
  "sent_at": "2026-08-14T16:00:01+08:00", // 完成时刻 RFC3339
  "new_session": true             // 经「新聊天→搜索」新建会话发送
}
```

### 3.2 `task:summary`（新增消息类型，任务收口必发一次）

任务无论正常完成、熔断、预热截止、设备失联还是未配置 IP，都会收口发一次；
同一任务续发后再次收口会重发（以最后一次为准）。

```jsonc
{
  "task_id": "...", "udid": "...", "serial": "...", "device_name": "...", "conn_type": "usb",
  "status": "completed | no_ip | device_unreachable | circuit_breaker | warmup_cap",
  "total": 100, "sent_ok": 80, "sent_fail": 5, "cancelled": 3, "pending": 12, // pending=未执行（含待续发）
  "start_at": "2026-08-14T15:59:50+08:00", "end_at": "...", "duration_ms": 35000,
  "reason": "熔断:连续失败 5 条"   // 人读原因，可空
}
```

### 3.3 `device_list` / `device:status`（补连接方式）

- `device_list` 每设备新增 `conn_type: "usb" | "wifi"`（USB 直连=usb，仅 Wi-Fi 健康=wifi）。
- `device:status` 新增 `conn_type`（状态变化时随 online/busy/offline 一起上报）。

### 3.4 可靠性

- 明细与汇总都是**先落盘再上行**（at-least-once）；`ReportQ`/`SummaryQ` 队列满时丢弃，
  云通道重连后 `ResendPersisted` 自动补报（明细 + 任务汇总一并补报）。
- 单条明细读取收件人姓名（`wda.ChatTitle`）为 best-effort：读不到返回空串，不重试、不影响发送链路。

## 4. 本地落盘格式（data/results/）

| 文件 | 内容 |
|---|---|
| `<task_id>.json` | 单条明细：旧 4 字段 + udid/conn_type/content/contact_name/sent_at/new_session（旧格式文件可直接读取续写） |
| `<task_id>.meta.json` | 任务级汇总（TaskSummary 原样落盘） |
| `metrics.json` | 每日分设备统计 + 历史（不变） |

## 5. 本地查询接口与 UI

- `GET /api/tasks`：任务列表（按更新时间倒序，最多 100 个；含汇总与计数）。
- `GET /api/tasks/{task_id}?offset=&limit=`：单任务逐条明细（默认 limit=500）。
- `GET /api/items?udid=&limit=`：**按设备分组**的跨任务发送明细（默认 3000 条，`truncated` 标记截断）。
  升级前落盘、缺 udid 的历史明细按 metrics.json 的 batch_id→udid 映射尽力归因（仅每设备最近一批），
  无法归因的归入 udid 为空的「未知设备」组并置于最后。
- `GET /api/devices`：每设备新增 `conn_type`。
- 管理页「发送明细」视图按设备分组展示（设备名/序列号 + 成功/失败/取消计数 + 逐条明细表，
  明细含 task_id 便于回平台核对）；设备卡片新增「明细」按钮直达该设备分组。
- **无手机号守卫**：平台下发明细缺手机号（空/纯空白）时直接标记失败并上行
  「明细缺少手机号，已拒绝发送」，不再走 WDA「默认会话」路径——避免消息发给当前打开的任意聊天、
  造成明细显示发送成功却无号码。

## 6. 未做项（后续按需）

- **电池电量 / WhatsApp 版本 / 登录状态**：WDA 需建会话后额外查询，且 `whatsapp_version` 目前
  无可靠来源（WDA /status 不含三方 App 版本），先留空不造假数据。
- **重试次数（attempts）**：当前发送链路无自动重试，字段无真实数据来源，等重试机制落地再加。
- **明细文件清理**：`data/results/` 目前无限累积（既有行为，未变）；任务量大后需要按保留期清理。

## 7. 云平台侧对接清单（待指令后实施）

1. `item:result` 解析新增字段 → 入库（明细表加列：udid/serial/device_name/conn_type/content/contact_name/sent_at/new_session）。
2. 新增 `task:summary` 消息处理 → 任务表更新最终态（counts + status + 起止时间 + 原因）。
3. `device_list`/`device:status` 的 `conn_type` → 设备展示。
4. 平台任务详情页按明细展示：谁（号码+姓名）、什么内容、什么结果、哪台设备、什么连接、何时。
