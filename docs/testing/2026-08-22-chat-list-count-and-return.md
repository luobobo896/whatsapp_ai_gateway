# 2026-08-22 聊天列表群发：人数、收件人、发完回聊天页

## 现象（HK `a5407b4` 留空群发后）

两台设备都发出了 2 条（网关 `chat_list_sent=2`），但：

- 平台任务进度是 `1 / 0 / 1`（按明细条数，不是人数）
- 收件人落成 `+ 8 6,1 5 2,...` 这种无障碍拆字
- 「聊天列表已发送 2 人」写在明细 `error`，界面列名还是「失败原因」
- 发完停在最后一条会话或中间页，没有回到聊天列表

LLM 配了，但失败日志里 `called=false`：`recordBug` 故意不在失败路径调模型；真正该调的是「点进会话却还在列表」时的 `DiagnoseScreen`。

## 修复

- 列表单元格优先用 `ChatSessionCell_Name`，并把拆开的号码收成 `+86 152 1347 2085`
- 发出后读聊天页标题作为收件人；整单结束后 `gotoChatList`
- 点进后先等输入框；没有则视觉诊断再试一次
- 平台明细增加收件人，「失败原因」改为「说明」，空号码显示「聊天列表」
- 上报 `contact_name` 形如 `2人：A、B`

## 命令

```bash
go test ./internal/wda/ ./internal/gateway/ -count=1 -run 'TestCompactChatTitle|TestChatDisplayTitlePrefersNameThenChild|TestFriendChatTargets|TestChatListOutcomeRemembersCount|TestParseSourceCells'
```
