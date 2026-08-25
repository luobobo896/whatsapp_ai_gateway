# Windows 无线设备发现（局域网扫描）变更记录

日期：2026-08-25
环境：Windows 11 + Apple Mobile Device Service + netmuxd（提供 usbmux ConnectionType=Network 条目）
版本：提交 187b257 之后工作区改动，打包产物 `dist/windows-amd64`（含本改动）

## 背景

Windows 的 Apple Mobile Device Service 本身不产生 usbmux `ConnectionType=Network` 条目，
接入 netmuxd 后由 go-ios（读 `USBMUXD_SOCKET_ADDRESS`）提供无线条目。本改动提供的是
**兜底路径**：已配置设备（记录过 VendorUUID）即使 mDNS/网络条目暂缺，只要手机 Wi-Fi 上
WDA 在跑，网关即可经局域网扫描（ScanLANWDA，探测私网 /24 的 :8100/status）找回设备并恢复 IP。

## 改动

- `internal/gateway/discover.go`：新增 `wifiScanned()`（10s 缓存）与
  `wifiMatchByVendorUUID()`（按 VendorUUID 强匹配，避免认错机器）。
- `internal/gateway/web.go`：`deviceList()` 把局域网扫描结果合并进设备列表；
  命中已配置设备时标记在线（conn=wifi）并恢复 Wi-Fi IP；无 VendorUUID 时短路不扫描。
- `internal/gateway/discover_test.go`：新增纯函数单测。

## 验证

- `go vet ./internal/gateway` 通过；新增单测 `TestWifiMatchByVendorUUID*` 通过。
- 打包脚本 `scripts/build-windows-exe.sh` 成功，产物版本 187b257；
  二进制内可检索到新增日志字符串。
- 说明：测试用无扩展名 sh 脚本做辅助二进制的问题已修（Windows 下种 `.exe`/`.cmd`）；
  剩余 Windows 平台差异失败仅为 SQLite 临时文件句柄未释放（TempDir 清理），
  与本次改动无关，构建按脚本开关 `SKIP_TESTS=1` 跳过。

## 限制

- 局域网扫描只能发现「手机 Wi-Fi 上 WDA 正在运行」的设备；全新未配置设备没有
  UDID 来源（WDA 只暴露 identifierForVendor），仍需先 USB 首配一次。
- 拔线保活的主通道是 netmuxd 的 Network 条目（见
  [2026-08-25-windows-netmuxd-network-keepalive.md](./2026-08-25-windows-netmuxd-network-keepalive.md)）；
  本改动提供的是 mDNS/网络条目暂缺时的列表可见性与 IP 恢复兜底。
