# WhatsApp 发送：已验证步骤（禁止再犯）

最后核验：2026-08-22，iPhone 7 Plus / iOS 15.8.8 / 普通 WhatsApp（非 Business）。  
成功证据：`wda-probe -phone 15213472085 -text 你好`，5.895s，`err=<nil>`，**未走模型**。

## 成功步骤（下次按这个做）

1. 先确认 WDA `/status` ready，再 `GET /source` 看**当前**是列表还是聊天页。不要只信代码报错。
2. 聊天页认这三个控件，不要猜旧 Appium 名：
   - 标题：`NavigationBar_ConversationHeader`（备用 `NavigationBar_TitleLabel` / `NavigationBar_HeaderViewButton`）
   - 输入框：`ChatBar_ComposerTextView`（空输入时右侧是语音键，不是发送键）
   - 发送键：打完字后才出现 `ChatBar_SendButton`
3. **已经在目标号码的聊天页 → 直接输入发送**。禁止先点导航栏「聊天」返回列表。
4. 标题里的 `+86 152 1347 2085` 带 LTR/NBSP，用抽数字比较（11 位或 86+11），不要字符串全等。
5. 列表里号码 cell 的 `name` 是 `+ 8 6,1 5 2,1 3 4 7,2 0 8 5`，同样抽数字匹配。
6. 主路径只用选择器。模型最多 4s；401/402/429 冷却 10 分钟。欠费不能挡发送。
7. 不要给「You / 你 / Message yourself / 给自己发」发。列表里名字旁的 `(你)` 徽章是「上一条是你发的」，**不是**自己的会话。

## 禁止再犯

| 错法 | 实际发生过的后果 |
|---|---|
| 标题读不到就报「找不到联系人」 | 人已经在聊天页里，代码点返回，再去列表/搜索，最后误报找不到 |
| 每条发送前无条件 `gotoChatList` | 把刚打开的目标会话关掉 |
| 用 `ChatTitleView_Title` / 裸 `TextView[1]` 当唯一依据 | iOS 15 WhatsApp 对不上，校验一直失败 |
| 把 `(你)` 徽章当成自己 | 会误伤普通会话 |
| 模型同步调 20s / 失败路径再调一次模型 | 欠费时主流程被拖死 |
| 会话开始时 dump 一次就当「当前页」 | dump 的是列表，发送时已经进了聊天页 |
| 只信 `deep link unsupported and no chat/contact` | 这句话经常是校验误判，先看屏幕/source |

## 复验命令

```bash
# WDA 已在手机 Wi-Fi :8100 时
go run ./cmd/wda-probe -wda http://<phone-ip>:8100 -phone 15213472085 -text '你好' -send
```

实现落在 `internal/wda/whatsapp.go`：`openTargetChat` 先 `chatOpenedFor`，命中就发。
