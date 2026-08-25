# 首次接入授权（无线调试开关）

日期：2026-08-25

写 `EnableWifiConnections` / `EnableWifiDebugging` 只允许 USB 首次授权，顺序不能反：

1. 必须插着 USB。
2. 手机已设置锁屏密码。
3. 点「首次授权」才写两个开关；手机会再弹密码框，只在手机上输入。
4. Network 激活不写这两个开关。未做 USB 授权则 Network 按钮不可用。

接口：`POST /api/devices/{udid}/authorize-wifi`。无 USB 直接失败。请求体不接受锁屏密码。

```bash
go test ./internal/gateway ./cmd/wifi-lockdown -count=1 -timeout 180s \
  -run 'TestParseWifiLockdownStatus|TestNeedWifiAuth|TestWifiDebugReadyOnlyFlag|TestEnableWifiLockdownRequiresUSB|TestEnableWifiLockdownMissingBinaryErrors|TestConfigPersistenceRoundtrip|TestIsPasscodeRequiredErr'
```
