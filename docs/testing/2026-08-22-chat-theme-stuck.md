# 2026-08-22 236 群发停在「聊天主题」

## 现象

7 Plus（236）群发点进会话后停在 WhatsApp **聊天主题**（壁纸/气泡设置），没有输入框，无法继续发。

## 根因

`gotoChatList` 返回键用了 `label CONTAINS '聊天'`。会话页上的「聊天主题」也会命中，返回时点进了设置页。该页导航栏标题是「聊天主题」，返回键是上一个会话名（如号码），没有 `ChatBar_ComposerTextView`。

真机源码树（脱敏）：`WDSNavigationBar` + StaticText `聊天主题` + 文案「聊天气泡和壁纸都会发生变化」。

## 修复

- 返回键排除 `主题`。
- 识别聊天主题页，点导航栏第一个按钮退出。
- 好友列表跳过「聊天主题」行。

```bash
go test -count=1 ./internal/wda/ ./internal/gateway/
```
