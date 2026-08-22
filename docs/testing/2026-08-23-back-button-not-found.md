# 2026-08-23 返回聊天列表 back button not found

## 现象

7 Plus 群发 `stage=chat_list`，错误 `back button not found`（约 2026-08-23 00:44）。LLM=true 但 `called=false`（失败路径不调模型）。

## 根因

`e1f47df` 为避开「聊天主题」把返回键改成：

`label CONTAINS '聊天' AND NOT label CONTAINS '主题'`

NSPredicate 里 `NOT` 必须写成 `NOT (label CONTAINS '主题')`，否则谓词无效，中文「聊天」返回键永远匹配不到。英文侧又从 `CONTAINS 'Chats'` 收成精确 `== 'Chats'`，带 RTL 不可见字符或 "Back to Chats" 时也会失败。无导航栏/坐标/滑动兜底。

## 修复

- 谓词 `NOT (...)` 括号；优先 `NavigationBar` 内按钮，避免点到会话页「聊天主题」。
- 回退：导航栏首按钮 → 底栏聊天 Tab → 导航栏左侧坐标 → 左缘右滑。
- 点击后确认消息输入框消失；若误入主题则 `leaveChatTheme`。

## 单测

```bash
go test -count=1 ./internal/wda/ ./internal/gateway/
```

关注：`TestIsBackToChatsLabel`、`TestWhatsappBackToChatsPredicatesUseGroupedNot`、`TestIsChatThemeTitle`。

## 真机手测

1. iPhone 7 Plus 打开任意会话（中文 WhatsApp）。
2. 触发聊天列表群发（空号码）或单发后观察是否回到列表。
3. 人为点进「聊天主题」后再发：应先退出主题再回列表，不再报 back button not found。
