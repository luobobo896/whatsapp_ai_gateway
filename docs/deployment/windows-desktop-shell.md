# Windows 桌面壳（WebView2 + 托盘）

对齐 macOS DMG 体验：双击 `WDAFarmGateway.exe` → 托盘常驻 + 自动弹出管理窗口，**不必再手动开浏览器**。窗口内仍是现有 `static/index.html`，监听 `0.0.0.0:8300`，局域网可访问。

## 选型

| 方案 | 结论 |
|---|---|
| **Go + jchv/go-webview2 + energye/systray**（采用） | 与现有 Go 栈一致；`CGO_ENABLED=0`；WebView2 随 Win10/11 常见；托盘纯 syscall |
| Wails / Fyne | 引入整套前端工具链，改动面大 |
| Lorca / 系统 Chrome | 依赖本机 Chrome，不像原生客户端 |
| 纯 C#/WinUI 壳 | 第二套工具链，维护成本高 |

## 目录布局（分发 zip）

```
dist/windows-amd64/
  WDAFarmGateway.exe   # 桌面壳（托盘 + WebView2）
  gateway.exe          # 现有网关（壳的子进程）
  wda-probe.exe
  static/index.html
  使用说明.txt
  （可选）bin/ios.exe、iproxy、idevice_id、ideviceinfo
  （可选）wda.ipa
```

## 行为对照（macOS Swift 壳）

| 能力 | macOS | Windows 壳 |
|---|---|---|
| 子进程 gateway | ✅ | ✅ 同目录 `gateway.exe` |
| 监听 | `0.0.0.0:8300` | 同左（`GATEWAY_PORT` 可覆盖） |
| WebView 加载 | WKWebView → `127.0.0.1:8300` | WebView2 → 同 URL |
| 就绪轮询 | `/api/cloud` 200/401 | 同左 |
| 托盘菜单 | 打开 / 复制 LAN / 重启 / 数据 / 日志 / 退出 | 同左（中文） |
| 单实例 | bundle id | 命名互斥量 `Local\WDAFarmGatewaySingleInstance` |
| 可写状态 | `~/Library/Application Support/WDAFarmGateway` | `%AppData%\WDAFarmGateway` |
| 关窗 | 不退出（托盘仍在） | 关 WebView 窗口不退出；托盘「退出」才停网关 |

## 构建

在 macOS / Linux 交叉编译（与 `gateway.exe` 相同）：

```bash
sh scripts/build-windows-exe.sh
# 产出含 WDAFarmGateway.exe
```

仅编壳：

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath \
  -ldflags "-s -w -H windowsgui" \
  -o dist/windows-amd64/WDAFarmGateway.exe ./cmd/wda-desktop
```

`-H windowsgui` 去掉黑色控制台窗口。

## 运行时依赖

1. **WebView2 Runtime**（Win11 / 较新 Win10 通常已带；缺失时壳会弹窗提示）  
   https://developer.microsoft.com/microsoft-edge/webview2/
2. 同目录 **`gateway.exe` + `static/`**
3. 激活链路仍要：Apple Devices/iTunes、`ios.exe`/`tidevice`、Windows `iproxy` 等（见 [windows-night-runbook.md](./windows-night-runbook.md)）——**壳不解决 USB/签名**

## 打包路线

| 阶段 | 产物 | 说明 |
|---|---|---|
| 现在 | zip（`dist/windows-amd64/`） | 解压即用；不签名 |
| 稍后 | MSI / 安装器 | 可用 WiX / Inno Setup；可捆绑 WebView2 Evergreen Bootstrapper |
| 稍后 | Authenticode 签名 | 避免 SmartScreen；需代码签名证书 |

敏感数据（`gateway.db`、云 token、`devices.json`）**不要**打进 zip。

## 阻塞项 / 已知限制

- **本机构交叉编译可出 exe**；真机 GUI（托盘 + WebView2）需在 Windows 上点开验收。
- **WebView2 Runtime** 缺失时无法开窗（有对话框）。
- **代码签名**未做 → 首次运行可能 SmartScreen 拦截。
- **libimobiledevice / ios.exe / iproxy** 仍不随包（与原先 gateway.exe 策略一致）。
- Windows 上对子进程发 `CTRL_BREAK` 做优雅停机；若无效，20s 后强杀（对齐 macOS 超时）。
- 关 WebView 窗口不会隐藏到托盘「再点恢复」以外的特殊动画；再开用托盘「打开管理页」。

## 源码

- `cmd/wda-desktop/`（`//go:build windows`）
- macOS 壳仍在 `desktop/`（Swift）
