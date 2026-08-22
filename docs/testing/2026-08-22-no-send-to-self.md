# 2026-08-22 禁止给自己发送

硬性规则：本机 WhatsApp「自己 / You / 给自己发消息」会话不发送。

- 聊天列表群发跳过该会话
- `TypeAndSend` 打开后若标题是自己，直接返回 `ErrSendToSelf`，不输入不点击
- 该失败不计入连续失败熔断

```bash
go test ./internal/wda/ ./internal/gateway/ -count=1
# ok  wda-farm-gateway/internal/wda       9.392s
# ok  wda-farm-gateway/internal/gateway   2.074s
```
