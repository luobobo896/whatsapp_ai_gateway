# 2026-08-23 iPhone WhatsApp 群发行为规则对齐

## 产品规则

1. **普通群发**：聊天 UI 内按收件人列表逐条发送。
2. **整单结束**：必须回到并停留在**聊天列表**，不得停在最后一条会话窗。
3. **指定号码**：仅当明确提供手机号时才允许「新聊天→搜索」；空号走聊天列表好友，禁止空搜。
4. **高版本 iOS（≥16.4）**：优先深链（`whatsapp://send?phone=` → `https://wa.me/`），失败再列表匹配/搜索。
5. **搜索异常**：搜索框输入号码后禁止静默挂起——必须打开会话，或关闭搜索并返回明确错误。

## 实现要点

| 规则 | 落点 |
|---|---|
| 1 | `Executor.processTask` 明细循环；空号 `SendToChatListFriends` 逐好友 |
| 2 | `GotoChatList` 导出；指定号码路径 `processTask` defer 回列表；空号路径函数尾已回列表 |
| 3 | `ShouldSearchContact`；`openNewChatByPhone` 仅在指定号深链/列表失败后调用 |
| 4 | `PreferDeepLink` + `whatsAppSendDeepLinks`；老系统先列表再深链 |
| 5 | `openNewChatByPhone` typed/opened + defer `dismissPicker`；错误含「未找到可发送联系人」等 |

复用/加固：`gotoChatList` 的 NSPredicate `NOT (...)`、导航栏/Tab/坐标/滑动兜底（见 `2026-08-23-back-button-not-found.md`）。

## 单测

```bash
go test -count=1 ./internal/wda/ ./internal/gateway/
```

关注：`TestPreferDeepLink`、`TestWhatsAppSendDeepLinks`、`TestShouldSearchContact`、`TestSearchMissErrorIsClear`、`TestWhatsappBackToChatsPredicatesUseGroupedNot`。

## 残余风险

- 真机回列表仍依赖无障碍树；极端主题页/弹窗可能仍 warn 后拆会话。
- 深链在部分老 iOS 上 HTTP 成功但不跳转，已靠 `chatOpenedFor` 校验后走列表/搜索。
- 搜索结果出得慢的机型仍固定 4s+2s 窗口，极慢网络可能误报未找到。
