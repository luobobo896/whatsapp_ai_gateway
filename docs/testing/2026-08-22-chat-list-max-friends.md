# 2026-08-22 聊天列表人数上限

未指定号码时最多发送 `web.chat_list_max_friends` 个 1:1 好友，默认 30，硬顶 100。

```bash
go test ./internal/wda/ ./internal/gateway/ -count=1
# ok  wda-farm-gateway/internal/wda       9.372s
# ok  wda-farm-gateway/internal/gateway   2.020s
```
