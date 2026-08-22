# 2026-08-22 未指定号码改走聊天列表好友群发

## 行为

- 明细手机号为空，或整单无明细但有任务文案：扫描聊天列表当前可见的 1:1 会话并逐个发送。
- 跳过筛选条、群（名称含群/Group/广播/频道等）、未知会话。
- 设备离线时仍按离线失败，不再用「缺少手机号」提前结束。
- 不滚动加载更早的会话，只发 accessibility 树里已经出现的条目。

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
