# 网关自主群发 Agent（deepseek-harness 核心 × 群发优化）

> 2026-08-26 · 范围：仅网关侧（whatsapp_ai_gateway）。
> 我们**不是 coding agent**，只做「群发」。只取 deepseek-harness 的**核心**，其余全部砍掉。

## 0. 定位

让网关在**不依赖云平台 `task:dispatch`** 的情况下，也能自主向 **WhatsApp 应用联系人
（聊天列表 1:1 好友）** 群发：主动观察设备/统计/时间窗/预算，决定「现在、用哪台设备、发多少条」，
然后交回现有 `Executor` 的确定性发送链路。

三条已确认的产品规则：

1. **目标是 WhatsApp 应用联系人** —— 不是外部传入的号码列表；`submit_batch` 不传 `phone`，
   `items` 留空，复用现有 `SendToChatListFriends`（`maxFriends` 默认 30，硬顶 100）。
2. **本地任务优先于云任务** —— per-UDID 队列拆成 agent 优先 + cloud 后备，worker 先消费本地任务；
   云任务仅当本地该 udid 无 pending 时才执行，冲突时走现有「队列满丢弃、平台重连补推」语义兜底。
3. **没有群发不触发事件** —— 预筛严格短路：无目标/无可用设备/非窗口/超预算 → 静默 `idle`，
   不调 LLM、不写决策事件（避免刷屏与空转成本）。

## 1. 从 deepseek-harness 只拿走三样东西

deepseek-harness 的哲学：**「调模型 → 跑工具 → 重复」之外的一切都属于插件。**
我们只保留这个最小循环，以及让它安全的两个伴生核心：

| deepseek-harness 核心 | 我们（群发版） | 取/舍 |
|---|---|---|
| agent loop（driver） | `agentLoop`：每 tick 一个 step（观察→决策→执行） | ✅ 取 |
| 受控工具管线（schema → pre-execute 门禁 → 单调 guard → execute → post-execute → result） | `agentTool` 管线 + `guard()` 单调守卫 | ✅ 取（安全关键） |
| 追加式会话日志（唯一真源，历史从日志派生） | `data/agent/history-*.jsonl` 追加式会话日志（审计/回放） | ✅ 取 |
| 工具 schema / `defineTool` 参数校验 | 每个工具带 JSON Schema + 参数校验 | ✅ 取 |
| `tools.restrict` 白名单 | 只暴露 4 个只读 + 1 个写工具 | ✅ 取 |
| Cordis / 插件化组合 | ❌ 不需要 | ❌ 舍 |
| 事件分类法 / agent/status / inbox | ❌ 不需要 | ❌ 舍 |
| UI (web/headless)、会话持久化恢复、fork | ❌ 不需要 | ❌ 舍 |
| code-mode / run_code / subagent / 文件系统 / shell | ❌ 我们是群发，不是 coding agent | ❌ 舍 |
| 审批流(approve)、多 mission 目录、成本上限、dry_run | ❌ 只做核心功能 | ❌ 舍 |
| 并行工具调用池 / 并发安全分类 | ❌ 串行即可 | ❌ 舍 |

> 「类似 Harness」在本文中指上面这个**模型驱动的受控循环**，不是某个具体产品。

## 2. 回路（简化到只剩核心）

```
tick（默认 60s，可配）
  observe   并行采集只读传感器
  prefilter 确定性短路（“没有群发不触发事件”）：
             未启用 / 无可用设备 / 非窗口 / 今日已超预算 / 设备 busy
               -> 静默 idle：不调模型、不写决策事件
  decide    一次 LLM 调用，返回结构化 JSON 决策
  guard     JSON Schema + 单调守卫（预算/窗口/白名单），不可被模型绕过
  act       合法 -> submit_batch（items 空=发应用联系人）-> Executor.Submit（幂等）
  log       追加决策/工具结果到会话日志 + 更新进度
```

预筛是**控制成本与风险、并满足“没有群发不触发事件”**的核心：绝大多数 tick 被确定性规则短路为
静默 `idle`，只有「确实可能要做」（窗口内 + 有健康设备 + 未超预算 + 应用联系人还有发送空间 + 无冲突）
才唤醒模型。

> deepseek-harness 的「调模型 → 跑工具 → 重复」在这里坍缩为「调一次模型 → 跑唯一的一个写动作」——
> 因为我们只有一个确定的写工具（`submit_batch`），把只读观测折叠成快照、再单调守卫一次，最贴合「只做核心」。

## 3. 观察与动作（只读快照 + 唯一写动作）

为了贴合「只做核心 + 非 coding agent」，我们用**一次决策**代替 deepseek-harness 的
「模型调工具」循环：只读传感器在 `observe` 折叠成一份状态快照（等价于 `list_devices` +
`get_metrics` + `get_time` + `get_plan`），直接注入模型上下文；模型只回一个结构化决定；
写动作只有 1 个。模型看不到文件/shell/网络/代码工具，也没有**传入号码列表**的入口。

| 只读传感器 | 折叠进快照的字段 | 类别 |
|---|---|---|
| `list_devices` | devices[].udid/ip/status/healthy | 只读 |
| `get_metrics` | devices[].today_sent/today_fail/today_new/remain | 只读 |
| `get_time` | now/timezone/in_window/window | 只读 |
| `get_plan` | content/daily_cap/max_friends/interval/burst | 只读 |

| 写动作 | 调用 | 守卫 |
|---|---|---|
| `submit_batch(udid)` | `Executor.Submit`（`Source="agent"`，`items` 空=发应用联系人） | 单调 guard + schema，越权/越预算/坏 intent 一律拒绝 |

`submit_batch` **没有 `items`/`phone`/`content` 参数**——目标是应用联系人，内容固定取自
`AutonomyConfig.Content`，发给谁由 `SendToChatListFriends` 决定（聊天列表 1:1 好友，
`MaxFriends` 上限）。模型只决定 `udid` 和 `intent`，无法改写话术或号码。

## 4. 决策 JSON 与守卫

模型每轮只回一个 JSON（不自由发挥）：

```jsonc
{
  "intent": "idle | submit",
  "udid": "4886579a...",
  "reason": "进入窗口，设备A在线，应用联系人可发 20 条（今日 12/40）"
}
```

`guard`（单调、不可被模型改写）强制：

- `intent` 白名单；空/越权任意值 → 拒绝。
- `udid` 必须是已配置且健康（online）的设备；busy 或 offline → 拒绝。
- `content` 固定取自 `AutonomyConfig.Content`，非空、长度上限（模型不能改话术）。
- 今日剩余 `DailyCap`（− 今日 `sent_ok`，含云任务已发）必须 ≥ 1；且 `MaxFriends` 未耗尽。
- `schedule` 与窗口/预热/新会话占比冲突时取更保守值（节流最终仍由 `Executor` 强制）。
- 幂等键 `auto-<sha(udid+date+content 前缀)>`：同一天重复决策 → 同 task_id，`Executor` 幂等，不会双发。
- 若该 udid 已有本地 pending / 正在 busy -> 直接 `idle`，不重复提交。

校验失败 → 记 `decision_rejected`，不调用 `submit_batch`。

## 5. 会话日志（唯一真源 / 审计）

`data/agent/history-YYYY-MM-DD.jsonl`，追加式，作为模型历史与审计来源；与发送结果
`results.db` 分开（后者记录真实发送，前者记录 Agent 决策）。**静默 `idle` 不写此日志。**

```jsonc
{"seq":1,"kind":"observe","state":{...},"time":"..."}
{"seq":2,"kind":"decision","intent":"submit","batch":{...},"reason":"..."}
{"seq":3,"kind":"guard","result":"accept|reject","reason":"..."}
{"seq":4,"kind":"tool","name":"submit_batch","result":"auto-...","time":"..."}
{"seq":5,"kind":"idle","reason":"outside window"}   // 仅当预警/决策存在但最终被判 idle 时记录
```

不记录 `api_key`；完整话术默认只存模板引用+变量指纹（可配置），避免敏感内容落盘。

## 6. 配置（最小）

存 `config` 表 key=`autonomy`（走现有 `ReadExtra`/`WriteExtra`）。

```go
type AutonomyConfig struct {
    Enabled            bool     `json:"enabled"`                 // 总开关，默认 false
    Content            string   `json:"content"`                 // 话术模板
    MaxFriends         int      `json:"max_friends,omitempty"`   // 应用联系人单批上限，默认 30，顶 100
    WindowStart        string   `json:"window_start,omitempty"`  // HH:MM；空=不限制
    WindowEnd          string   `json:"window_end,omitempty"`
    IntervalSec        int      `json:"interval_sec,omitempty"`  // 默认 20
    BurstCount         int      `json:"burst_count,omitempty"`   // 默认 5
    BurstPauseSec      int      `json:"burst_pause_sec,omitempty"` // 默认 60
    DailyCap           int      `json:"daily_cap,omitempty"`     // 默认 40
    MaxNewSessionRatio int      `json:"max_new_session_ratio,omitempty"` // 默认 30
    TickInterval       int      `json:"tick_interval,omitempty"` // 默认 60
}
```

没有 `phones`（目标是应用联系人）、没有 mission 目录、没有审批、没有 dry_run、没有成本上限。
`Content` 支持模板变量 `${name}` / `{name}`（按联系人名渲染）；`MaxFriends` 为单批应用联系人上限；
`DailyCap` 为每日硬上限（单批 = min(MaxFriends, 今日剩余)，含云任务已发，绝不超发）。

**去重窗口（同一设备发给同一联系人后多久不再重发）** 不放在 `AutonomyConfig`，而是作为**全局发送行为**
放在 `WebConfig.ChatListRepeatDays`（与 `ChatListMaxFriends` 同层），作用于**所有**聊天列表好友群发
（含云任务），默认 **3 天**，滚动窗口（非自然日/日历周）：

```go
type WebConfig struct {
    // … 既有字段
    ChatListRepeatDays int `json:"chat_list_repeat_days,omitempty"` // 默认 3；<=0 回落默认
}
```

### 6.1 管理页 / REST 配置入口

| 接口 | 方法 | 说明 |
|---|---|---|
| `/api/autonomy` | GET | 读取自主群发配置（含 `chat_list_repeat_days`；无密钥） |
| `/api/autonomy` | PUT | 更新并持久化；**启用前必须填写 `content`**（否则 400）；`chat_list_repeat_days<=0` 时回落默认 3 |
| `/api/autonomy/status` | GET | 运行态 + **“为什么没发”诊断** + **最近一次自主任务的真实结果回填**：`state`（`disabled/outside_window/no_device/budget_reached/already_sent/rejected/submitted`）+ `reason` + `today_sent/daily_cap/ready_devices/window/in_window/running/llm_enabled` + `last_task/last_task_result`（如“已发送 3 个应用联系人”“没有新的未触达联系人（均已发过或在重复触达间隔内）”“发送失败：…”“发送中…”） |

管理页「自主群发」卡片（`static/index.html`）会按 `state` 上色展示未发原因：

| state | 界面语义 |
|---|---|
| `disabled` | 未启用（需开启并保存话术） |
| `outside_window` | 发送窗口外 |
| `no_device` | 没有在线且预算未满的设备（检查 WDA 是否在线） |
| `budget_reached` | 设备在线但今日预算已满 |
| `already_sent` | 今日这些设备已发过一批，重复触达间隔内不再重发 |
| `rejected` | 发送被拒绝（模型决策不合规/越权/话术为空） |
| `submitted` | 已提交；`last_task_result` 会回填执行器真实结果（已发 N / 无新联系人 / 发送失败 / 发送中），前端不再靠“猜” |

## 7. 与现有 Executor / 云平台兼容（本地优先）

- per-UDID 队列拆成 **agent 优先队列 + cloud 后备队列**；worker 先消费本地队列。`TaskDispatch`
  增加 `Source` 字段（`"agent"` / 空=cloud），`Executor.Submit` 据此入队到对应队列。
- **本地优先云任务**：云 `task:dispatch` 到达时，若该 udid 的 agent 队列有 pending 或正在 busy，
  云任务进入后备队列（or 走现有「队列满丢弃、平台重连补推」语义），不抢占本地任务。
- `submit_batch` 构造 `TaskDispatch{TaskID:"auto-"+hash, Source:"agent", Items:nil, ...}`
  送入 `Executor`，复用同一窗口/预热/熔断/新会话占比/结果持久化/补报规则。
- 云断开：agent 继续自主发送；恢复后 `ResendPersisted` 把 agent 已发明细一并补报（同在 `results.db`）。
- 上行协议不变（`item:result`/`task:summary` 照旧，`source` 为 omitempty，平台旧版忽略）。

## 8. 安全

- 模型输出不可信：所有 `submit_batch` 参数经 schema 校验 + 单调守卫；guard 不可被模型改写。
- 工具白名单：只有上述 5 个工具，无文件/shell/网络/代码访问，无传入号码列表入口。
- 幂等 + 审计：确定性 task_id + Executor 幂等 + 会话日志全部落盘，无密钥/完整话术。
- 默认关闭：`Enabled=false`；窗口/日预算/预热/新会话占比在 Executor 与 guard 双层强制，模型不能绕过。
- 超时/冷却：LLM 调用带超时 + 沿用现有 401/429/欠费冷却；`max_tokens` 小；预筛短路省调用。

## 9. 落点（文件改动）

- 新增 `internal/gateway/autonomy.go`：`AutonomyLoop`（tick -> 观察快照 -> 预筛 -> 决策 -> 守卫 ->
  `submit_batch` -> 会话日志）；传感器折叠进 `observe`，不建单独工具注册表。
- `internal/gateway/llm.go`：给 `LLMClient` 增加公开 `Decide(ctx, sys, user) (json.RawMessage, error)`
  （复用 `chat` + 结构化解析/截断）。
- `internal/gateway/config.go`：新增 `AutonomyConfig` + `ReadExtra`/`WriteExtra` 读写 `autonomy`。
- `internal/gateway/executor.go`：per-UDID 队列支持 agent 优先 + cloud 后备，`TaskDispatch.Source` 路由。
- `internal/gateway/gateway.go`：加 `Autonomy *autonomyLoop`；`New(...)` 装配；`main.go` 启动
  `Autonomy.Start(ctx)`，退出 cancel。
- 测试：`autonomy_guard_test.go`（越权/超预算/空 content/坏 intent 均拒绝）、
  `autonomy_prefilter_test.go`（非窗口/无设备/超预算→静默 idle 不调模型）、
  `autonomy_loop_test.go`（注入 fake planner/sensors）、`executor_priority_test.go`（agent 优先云）。

## 10. 验收（闭环）

1. `go test ./internal/gateway/... -count=1` 全绿 + 新增用例通过。
2. `enabled=true` + 有 `content`：窗口内 + 设备在线 + 未超预算时，tick 产出 `auto-*` 任务到
   `Executor` 队列，`/api/tasks` 可见；实际按聊天列表好友群发（`SendToChatListFriends`）。
3. 非窗口 / 关闭 / 无健康设备 / 超预算 / `MaxFriends` 耗尽 → 预筛**静默 `idle`**，不调模型、不写决策事件。
4. 伪造模型输出（越权 udid / 超预算 / 空 content / 坏 intent）→ guard 拒绝并记录 `rejected`，不发送。
5. 与云任务：本地 pending 时云任务后置不透发；重复 tick 幂等（同 udid+date+content 同 task_id）。
6. 断开云通道后 agent 仍自主发送，恢复后补报明细。

## 11. 已确认决策（对应你的三条）

| 你的要求 | 落地 |
|---|---|
| 发 whatsapp 应用联系人做群发 | `submit_batch` 不传 `phone`，`items` 为空，复用 `SendToChatListFriends`（`maxFriends` 默认 30 顶 100）；`get_plan` 返回「应用联系人可发上限」 |
| 本地任务优先云任务 | per-UDID 队列 agent 优先 + cloud 后备，worker 先消费本地任务；云冲突走「丢弃/补推」兜底 |
| 没有群发不触发事件 | 预筛严格短路为**静默 `idle`**，不调用 LLM、不写决策事件 |

> 其余（审批、成本、UI、mission 目录、dry_run、code-mode、subagent、传入号码列表）都按“只要核心”**不做**。

## 12. 本次缺口修复状态（2026-08-26）

按产品评审补齐，均已实现并测试：

| 缺口 | 落地 | 验证 |
|---|---|---|
| **P0 配置入口** | `GET/PUT /api/autonomy`（脱敏，无密钥）+ `GET /api/autonomy/status` + 管理页「自主群发」卡片 | `web.go` + `static/index.html` |
| **P0 预算硬约束** | `submit` 单批 = `autonomyBatch(DailyCap, MaxFriends, 今日已发)`，剩余含云任务，绝不超发 | `autonomy_test.go::TestAutonomyBatchHardCap` |
| **P0 LLM 故障降级** | 模型不可用/失败走确定性兜底：选剩余预算最大的就绪设备直接发；不再静默停摆 | `autonomy_test.go::TestAutonomyFallbackPicksLargestRemain` |
| **P0 为何未发可见** | `AutonomyLoop.Status()` 返回启用/运行/预算/窗口/就绪设备/诊断原因；静默 `idle` 也记录原因 | `autonomy_test.go::TestAutonomyStatusReport` |
| **P1 跨天不重复** | `results.db` 新增 `sent_contacts(udid,identity,sent_at)`；按 identity（11 位号）跳过；去重窗口默认 **3 天**，可用 `WebConfig.ChatListRepeatDays`（管理页「重复触达(天)」）配置 | `sent_contacts_test.go` / `whatsapp_chatlist_test.go` |
| **P1 上行 source** | `ItemResult`/`TaskSummary` 增加 `source`（`"agent"`/空），平台可识别自主任务；`omitempty` 旧版忽略 | 全量测试 |
| **P1 模板变量** | 话术支持 `${name}` / `{name}` 按联系人名渲染 | `whatsapp_chatlist_test.go::TestRenderChatContent` |
| **P1 多设备分摊** | **本次未做**：需改变模型决策 schema（一次多台）与批量分摊逻辑，与「模型只决定一台=安全边界」冲突，成本高，列为后续增强 | — |

**验收证据**：`go test ./... -count=1` → 286 passed / 9 packages；`go build ./...`、`go vet ./...` 干净。

**仍未覆盖的真机项**：未做 WDA 真机冒烟（联系人生成/发送的端到端）。上线前建议先用 `enabled=false`
接一台已验证设备，手动放一轮确认 `sent_contacts` 记录与跳过逻辑，再放开。

> **探活行为**：`wdaProbeVia` 不再"只认 USB 隧道"——USB 通道若无隧道但设备配了 Wi-Fi IP，
> 会回退到 `ip:port` 探测 `/status`（`wdaBaseURLFor` 同理）；无隧道又无 IP 才判 `USB 通道无隧道且未配置 IP`。
> 这样"已联网但没建 iproxy 隧道"的设备不会被误判离线。USB/Network 的激活通道互斥语义不受影响。

## 13. 管理页实时任务通知（WebSocket）

**背景**：云平台任务下发后，运营要在管理页**立即**看到"收到任务 → 执行中转圈 → 结束"，而不是等 5 秒轮询。

**方案**：网关作为宿主新增一条**管理页 ↔ 网关**的 WebSocket 通道（复用现有 `github.com/coder/websocket`
依赖），把任务事件**实时**推给管理页；原有 5 秒轮询保留为兜底。

> 注意方向：网关 ↔ 云平台是**网关作为 WSS 客户端**，方向不同，不能直接给浏览器用。这里新增的是
> **网关作为 WebSocket 服务端**的管理页订阅通道，复用的是同一套 WebSocket 技术栈与依赖，而非那条云通道。

### 13.1 事件中心与端点

- `EventHub`（`internal/gateway/eventhub.go`）：维护已连接的管理页连接，提供 `Publish(type, payload)`
  （best-effort 广播，慢/断开的连接跳过），并广播 `{"type","payload","at"}`。
- `GET /api/ws`（`internal/gateway/web.go`）：`websocket.Accept` 升级后注册到 `EventHub`；服务端仅做
  **读循环感知断开**（管理页不在该连接上发业务消息）。
- 鉴权：该路由走 `Handler` 的统一 `/api/*` 中间件——`auth.Authenticated(r)`（session cookie）通过才放行，
  GET 不需 CSRF；未登录握手被 401 拒绝。同源管理页带 cookie 可连。

### 13.2 触发点（下行事件）

| 事件 | 触发 | payload |
|---|---|---|
| `task:new` | 云通道收到 `task:dispatch`（`cloud.go`） | `{task_id, udid, item_count, source}` |
| `task:cancel` | 云通道收到 `task:cancel` | `{task_id}` |
| `task:done` | `Executor.finishTask` 收口（`executor.go`，经 `SetEventSink` 回调） | `{task_id, udid, status, source, sent_ok, sent_fail, cancelled}` |

装配：`Gateway.New` 创建 `EventHub` 并 `exec.SetEventSink(g.Hub.Publish)`（收口事件回流到事件中心，不阻塞发送链路）。

### 13.3 前端交互（`static/index.html`）

- `connectTaskWS()`：页面加载即 `new WebSocket((https?'wss':'ws')+'://'+host+'/api/ws')`。
- `onmessage`：`task:new` → 铃铛 badge + toast「收到云平台任务」+ 刷新任务实况；`task:done` → toast「任务结束：成功 X，失败 Y」；
  `task:cancel` → toast「任务已取消」。
- **重连**：`onclose` 后 3 秒重连；**鉴权兜底**：未登录握手 401 → 关闭 → 重连，登录成功 cookie 带上后再连上。
- **轮询兜底**：原 `refreshAll()`（5s）保留，WS 断开/未连接时仍能靠 `/api/tasks`（含 `running`）看到任务与结束状态。

### 13.4 边界

- 推送**实时、无补发**：若连接短暂断开恰好漏掉一个事件，重连后下一次事件才收到；期间靠 5 秒轮询兜底，信息不丢。
- `EventHub.Publish` 写超时 3s，慢连接跳过（不影响发送）。
- `Accept` 使用 `coder/websocket` 默认（未做 Origin 白名单穷举）；`/api/ws` 已要求登录，属本地网关工具场景。
- 前端实况面板的转圈/进度来自 `/api/tasks` 的 `running` + `Summary`；首条发出前可能显示 `0/N`。

> **跨平台：业务一致、实现因平台而定**。自主群发的**业务行为**（自主回路、探活回退 USB 无隧道→`ip:port`、
> 已发联系人去重、`/api/ws` 实时通知）在 Mac/Windows 一致；新增逻辑位于通用文件、**未新增平台分叉**。
> **实现层面按平台而定**（既有平台实现：Windows 走 netmuxd / usbmux 套接字与工具链、USB/Network 激活通道互斥，
> Mac 用系统 usbmuxd 等），交付时按平台选用对应实现。与 README / `usb-network-activate.md` 的
> "业务规则相同、平台只差实现"口径一致——即"业务一致、实现因平台"。

### 13.5 验证（可复现，不需真机）

```bash
# 1) 事件中心广播到 WebSocket 客户端的闭环测试
go test ./internal/gateway/ -run TestEventHubBroadcastOverWS -v

# 2) 手动自检：起网关后，浏览器打开管理页（登录），控制台执行：
#    const ws = new WebSocket((location.protocol==='https:'?'wss':'ws')+'://'+location.host+'/api/ws')
#    ws.onmessage = e => console.log('任务事件', e.data)
# 然后在云平台下发一个任务，管理页应：铃铛 🔔 亮起 + toast「收到云平台任务」+ 任务实况出现转圈项。

# 3) 若用真机/假 WDA：跑闭环 e2e（USB 通道）验证发送链，再手动在云平台下发看前端实时提醒。
go test ./internal/gateway/ -run TestAutonomyEndToEndSend -v
```
