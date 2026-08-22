# 2026-08-22 Windows WDA 激活后端

## 环境

- 机器：本机 macOS，仓库 `/Users/hanson/work/个人文档/whatsapp_ai_gateway`
- 未连接 Windows 主机，未做真机激活

## 命令

```bash
cd /Users/hanson/work/个人文档/whatsapp_ai_gateway
go test ./internal/gateway/ -count=1 -run 'TestResolveActivator|TestGoiosArgs|TestTideviceArgs|TestProtocolCmdMissingBinary|TestWDABundleIDDefault|TestPingArgs'
```

## 结果

```text
ok  wda-farm-gateway/internal/gateway  0.350s   # 上述 -run 过滤
ok  wda-farm-gateway/internal/gateway  1.792s   # ./internal/gateway 全量
```

2026-08-22 本机 macOS 两次均通过。

## 未覆盖

- Windows 上 `ios runwda` 拉起真机 WDA
- iOS 17+ wintun 隧道
- 交叉编译 `gateway.exe` 后的安装包
