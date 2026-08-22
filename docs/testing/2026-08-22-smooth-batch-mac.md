# 2026-08-22 Mac 连发（整单复用 WDA 会话）

对照竞品视频：同一条 XCTest/WDA 常驻，连打文本，约 1 秒间隔。  
本轮先在 Mac USB 真机把「每条都 CreateSession」改掉，再测 3 条。

## 改动

- `CreateWhatsAppSession` / `OpenChatOnSession`：打开号码不再建新会话
- 执行器按号码群发：整单共用一条会话，瞬时故障才拆掉重建
- `wda-probe -count N -interval S`：同一会话连发

## 验证

设备：iPhone 7 Plus / iOS 15.8.8，USB `127.0.0.1:18100`，测试号 `15213472085`。

```bash
go test ./internal/wda/ ./internal/gateway/ -count=1
# ok wda / ok gateway

go run ./cmd/wda-probe -wda http://127.0.0.1:18100 \
  -phone 15213472085 -text 'hello nice to see you' \
  -send -count 3 -interval 1 \
  -shot docs/testing/2026-08-22-smooth-batch-mac \
  -report docs/testing/2026-08-22-smooth-batch-mac/report.json
```

| 条 | 打开 | 输入+发送 | 单条合计 |
|---|---|---|---|
| #1 | 1019ms | 4604ms | 6437ms |
| #2 | 1065ms | 4583ms | 5648ms |
| #3 | 1253ms | 4732ms | 5985ms |

建会话 16ms（WhatsApp 已在前台）。整批 20.3s（含 2×1s 间隔）。  
截图 `03-sent.png`：15:22 三条绿泡均有勾，输入框空。

对比上一轮单条 27s（几乎全耗在 CreateSession 冷启动）。

## 和竞品仍有的差距

竞品视频约 1s 一条。我们热路径约 6s：约 1s 确认已在目标聊天，约 4.6s 输入+点发送+确认清空。  
没有为了追 1s 去掉 `confirmSent`（去掉会误报已发送）。

Windows 真机激活仍未核验，见 [windows-night-runbook.md](../deployment/windows-night-runbook.md)。
