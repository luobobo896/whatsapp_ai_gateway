# 2026-08-22 发送校验修复 + Windows exe 打包

## 问题

群发后手机跳到「搜索联系人」，或明明通讯录里有人却打开「未知」会话，后续明细连续失败，看起来只能下一台/一条。

## 根因（代码）

1. `chatOpenedFor` 在标题无数字时直接当成功：空标题、「未知/Unknown」、上一会话姓名都会放行。
2. 聊天列表只比对 cell `name` 的完整 86+11 位；已保存联系人 `name` 是姓名、号码在 `label` 或只有 11 位，匹配失败后掉进新聊天搜索。
3. 搜索页没有消息输入框，旧 `gotoChatList` 当成「已在列表」直接返回，下一条再也回不去。
4. 搜索结果点 cell 右侧，容易点到「给未保存号码发消息」。

## 命令

```bash
cd /Users/hanson/work/个人文档/whatsapp_ai_gateway
go test ./internal/wda/ ./internal/gateway/ -count=1
sh scripts/build-windows-exe.sh
```

## 结果

```text
go test ./internal/wda/ ./internal/gateway/ -count=1
ok  wda-farm-gateway/internal/wda       9.354s
ok  wda-farm-gateway/internal/gateway   1.883s

SKIP_TESTS=1 sh scripts/build-windows-exe.sh
dist/windows-amd64/gateway.exe: PE32+ executable (console) x86-64, for MS Windows
12M
```

未在真机 WhatsApp 上复跑群发；Windows 激活链路仍需目标机上的 Apple Devices + ios.exe。
