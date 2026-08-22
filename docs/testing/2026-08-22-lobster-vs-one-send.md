# 2026-08-22 对照龙虾视频：现有设备跑通一条

对照对象：用户视频里的 WS龙虾（打开会话 → 输入 `hello nice to see you` → 点发送 → 气泡出现、输入框清空、任务回传成功）。  
参考项目：[ShawnPana/phone-harness](https://github.com/ShawnPana/phone-harness)（镜像窗口 + OCR + HID 点按；本仓库**没有**接入它当发送驱动）。

## 结论

指定号码发一条文本，现有 **WDA 网关发送链路已经能对上视频主路径**。  
本轮 **没有走 LLM**，也没有用 phone-harness。LLM 仍然只是选择器失败时的视觉兜底，不能代替龙虾。

## 本轮设备

| 项 | 值 |
|---|---|
| 设备 | USB 在线的 iPhone 7 Plus（`iPhone9,2` / iOS 15.8.8） |
| UDID 前 8 位 | `59524996` |
| WDA | USB `iproxy` `127.0.0.1:18100` → 手机 `:8100`；`/status` ready |
| 手机 Wi-Fi IP | `192.168.20.165`（`devices.json` 里旧的 `192.168.1.11` 当时不通） |
| 收件人 | 已验证测试号 `15213472085`（playbook 同号，存量会话） |
| 文案 | `hello nice to see you`（与视频一致） |

未启动带云通道的网关，避免本机被平台下发真实群发任务。回传是探针本地 JSON。

## 命令

```bash
# 已有 xctestrun 时激活 WDA（USB 已插）
iproxy -u <udid> 18100:8100
xcodebuild -xctestrun <derived>/WebDriverAgentRunner_iphoneos18.5-arm64.xctestrun.59524996.runtime.xctestrun \
  -destination id=<udid> test-without-building

go run ./cmd/wda-probe -wda http://127.0.0.1:18100 \
  -phone 15213472085 -text 'hello nice to see you' -send \
  -shot docs/testing/2026-08-22-lobster-vs-one-send \
  -report docs/testing/2026-08-22-lobster-vs-one-send/wa-report-20260822.json
```

## 逐步结果（phone-harness：每步以屏幕为准）

| 步骤 | 视频龙虾 | 本轮 |
|---|---|---|
| 打开会话 | 进入 `+1 (702) 55…` | 进入 `+86 152 1347 2085`，`new_session=false`，20.3s。见 `02-opened.png` |
| 输入 | 输入框出现 `hello nice to see you` | 同一文案；发送键随后出现 |
| 发送 | 点纸飞机，气泡出现，输入框清空，单勾 | 气泡 `hello nice to see you` 15:09 单勾，输入框已空。见 `03-sent.png` |
| 回传 | 显示器 RPA「成功」 | `status=sent`，`total_ms=26752`，`send_ms=4794`。见 `wa-report-20260822.json` |

```text
WDA OK: ready=true os=15.8.8 ip=192.168.20.165
opened: title="+86 152 1347 2085" new_session=false duration=20.302s
send done: phone="15213472085" err=<nil> duration=27.005s
SEND OK
```

`go test ./internal/wda/ ./internal/gateway/ -count=1`：两包通过。

## 和龙虾 / phone-harness 的差距

| 点 | 龙虾视频 | phone-harness | 本仓库本轮 |
|---|---|---|---|
| 控机 | 手机本机 App + iOS Automation + `WA.js` | Mac 镜像窗口 OCR + CGEvent | Mac WDA 选择器（USB 隧道） |
| 眼睛 | 无障碍 + 脚本日志 | Vision OCR | Accessibility ID（`ChatBar_*`） |
| 群发 100 号 / 1 秒间隔 | 视频在跑 | 不负责 | 执行器有 `interval_sec`，本轮只发 1 条 |
| 云端任务大盘 | 有 | 无 | 有（`task:dispatch` / `item:result`），本轮故意没连云 |
| LLM | 不用 | Agent 写脚本、图标靠视觉 | 未调用；选择器成功 |

打开会话 20s 偏慢，主要是刚拉起 WDA / 切到 WhatsApp；真正输入+发送约 4.8s。龙虾视频约 1 秒一条，是机上脚本连打，不是模型。

## 不采用 phone-harness 当发送驱动的原因

1. 现有农场已经是 WDA，再加镜像/OCR 是第二套体系。
2. 它明确要求发消息前停下来问人，不是群发执行器。
3. iPhone 一次一台、依赖镜像窗口，对不上多机 USB 农场。
4. 借鉴的是「截图/界面当事实、做完再验」，已落到 `wda-probe -shot/-report`。
