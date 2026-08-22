# 2026-08-22 未指定号码改走聊天列表好友群发

## 行为

- 明细手机号为空，或整单无明细但有任务文案：扫描聊天列表当前可见的 1:1 会话并逐个发送。
- 跳过筛选条、群（名称含群/Group/广播/频道等）、未知会话。
- 设备离线时仍按离线失败，不再用「缺少手机号」提前结束。
- 不滚动加载更早的会话，只发 accessibility 树里已经出现的条目。
- 2026-08-22：平台若把本机号当成指定号码，新聊天只会搜到「给自己发消息」然后卡住。现改为回退聊天列表好友。云上报把 USB 隧道视为在线，避免枚举空时只给一台下发。
- 2026-08-22：用户留空仍带本机号，根因是香港平台把空号码展开成 `conversations.customer_phone`。网关回退只能避免卡死，挡不住输入框先打出该号。正确修复见平台 `docs/testing/2026-08-22-blank-phone-no-crm-expand.md`：留空只下发空明细。

## 命令

```bash
go test ./internal/wda/ ./internal/gateway/ -count=1
```

## 结果

```text
ok  wda-farm-gateway/internal/wda       9.389s
ok  wda-farm-gateway/internal/gateway   2.126s
```

未在真机 WhatsApp 上复跑无号码群发。
