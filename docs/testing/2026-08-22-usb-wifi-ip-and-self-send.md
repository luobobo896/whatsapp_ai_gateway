# 2026-08-22 USB 设备 Wi-Fi IP 错显 + 群发本机号

## 现象

1. 群发 `8618078526388` 失败，报 `deep link unsupported and no chat/contact`（13s，iPhone 7 Plus USB）。
2. USB 已连接的 iPhone Plus-2 界面仍显示 Wi-Fi `192.168.20.165`。

## 取证

- 7 Plus WDA `/status` ios.ip = `192.168.10.236`。聊天列表没有 18078526388；新聊天搜该号只出现「给自己发消息」。这是本机 WhatsApp 号（资料名罗泓森）。
- Plus-2 USB 隧道 `:53295` 的 WDA 自报 `192.168.10.237`，但 `devices.ip` 钉在 `192.168.20.165`。该地址来自 EasyTier 代理网段 `192.168.20.0/24` 的 follow。`00008120-...` 幽灵行还占用了 `192.168.10.237` 和同一 vendor_uuid。

## 修复

- USB 隧道探活成功则用 WDA 自报 IP 覆盖配置，并清掉其它设备抢占的同 IP / vendor_uuid。
- follow 优先 `ios.ip`，禁止抢 USB 设备已占用的 Wi-Fi IP。
- 新聊天搜索若只命中自己，返回 `ErrSendToSelf`，不再伪造成「找不到联系人」。

## 验证

```bash
go test ./internal/wda/ ./internal/gateway/ -count=1
```

```text
ok  wda-farm-gateway/internal/wda       9.445s
ok  wda-farm-gateway/internal/gateway   1.668s
```

代码需重新编译并重启桌面客户端后才会改正在跑的网关。重启后看护一轮应把 Plus-2 的 IP 改成 `192.168.10.237`。群发请改用聊天列表里真实存在的号码（如 152 / 176），不要对本机号发。

## Bug3 无法删除设备

正在跑的桌面客户端 `delDevice` 仍调用浏览器原生 `confirm()`。WebView 一旦拦截对话框，点「删除」完全没反应。源码已改为页面内确认框，删除与隐藏一次落盘，云上报不再带已隐藏设备。
