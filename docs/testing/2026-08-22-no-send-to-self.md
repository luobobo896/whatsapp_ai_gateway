# 2026-08-22 禁止给自己发送

硬性规则：本机 WhatsApp「自己 / You / 给自己发消息」会话不发送。

- 聊天列表群发跳过该会话
- `TypeAndSend` 打开后若标题是自己，直接返回 `ErrSendToSelf`，不输入不点击
- 该失败不计入连续失败熔断
- 2026-08-22 补：iPhone 7 Plus 上搜本机号 `8618078526388` 只会出「罗泓森 (你), 给自己发消息」。旧逻辑报 `deep link unsupported`，现按 `ErrSendToSelf` 拒绝。聊天列表 `已发送到你` 也视为自己。

```bash
go test ./internal/wda/ ./internal/gateway/ -count=1
# ok  wda-farm-gateway/internal/wda       9.445s
# ok  wda-farm-gateway/internal/gateway   1.668s
```
