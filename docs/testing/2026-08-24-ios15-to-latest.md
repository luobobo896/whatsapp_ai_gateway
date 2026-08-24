# 2026-08-24 iOS 15 到最新版本激活分流

- 日期：2026-08-24
- 目的：按 TesterHome / go-ios 文章把 iOS 17+ 隧道补进产品路径，声明支持 iOS 15 → 当前最新正式版
- 环境：本机 macOS，bundled go-ios 1.3.2；USB 在线一台 iPhone 7 Plus / iOS 15.8.7

## 实现

激活按 `ProductVersion` 主版本分流：

| 版本 | 前置 | 拉起 |
|---|---|---|
| 15–16 | Mac：Xcode DDI；Windows：`ios image auto` | `wifi-runwda` 或 `ios runwda` / tidevice |
| 16+ | `ios devmode get`，未开则失败 | 同上 |
| 17.4+（含 18 / 26） | 看护 `ios tunnel start --userspace`，等到该机进隧道列表 | 强制 go-ios `install`/`runwda`，带 `--tunnel-info-port=28100` |
| 17.0–17.3 | 用户态隧道不受 go-ios 支持 | 明确报错，要求升级或管理员内核隧道 |

不引入 Airtest，不把 go-ios 五十多条命令都包一遍。

## 命令与结果

```bash
go test ./internal/gateway/ ./internal/wda/ ./cmd/wifi-runwda -count=1
go build -o /tmp/wda-gateway-build ./cmd/gateway
```

2026-08-24：gateway/wda/wifi-runwda 测试通过；`./cmd/gateway` 编译通过。

新增单测覆盖：版本门槛、隧道参数、隧道 JSON、iOS 17 跳过 wifi-runwda、iOS 17 install 带端口 / iOS 15 不带。

`ios list --details` 读到本机 USB：`ProductVersion=15.8.7`，`Udid=5060c403…`。按分流这台不应起隧道。

## 未覆盖

- 本机没有 iOS 17.4+ 真机，隧道等到设备进列表、以及 17+ 发出一条消息，未实机核验
- Windows 真机激活仍未核验
- 未下载、未安装 wintun（17.4+ 用户态路径不需要）
