# 首次接入授权（无线调试开关）

日期：2026-08-25

写 `EnableWifiConnections` / `EnableWifiDebugging` 只允许 USB 首次授权，顺序不能反：

1. 必须插着 USB。
2. 手机已设置锁屏密码。
3. 点「首次授权」才写两个开关；手机会再弹密码框，只在手机上输入。
4. Network 激活不写这两个开关。

> 2026-08-25 补充：本机能以 Network 发现设备（配对记录在
> `C:\ProgramData\Apple\Lockdown`、netmuxd 挂上 `ConnectionType=Network` 条目）
> 就说明此前已授权过，**无需再插 USB 重复授权**，可直接 Network 激活。
> 只有全新设备（本机无配对记录、usbmux 无 Network 条目）才需要先走上面的 USB 授权流程。

接口：`POST /api/devices/{udid}/authorize-wifi`。无 USB 直接失败。请求体不接受锁屏密码。

```bash
go test ./internal/gateway ./cmd/wifi-lockdown -count=1 -timeout 180s \
  -run 'TestParseWifiLockdownStatus|TestNeedWifiAuth|TestWifiDebugReadyOnlyFlag|TestEnableWifiLockdownRequiresUSB|TestEnableWifiLockdownMissingBinaryErrors|TestConfigPersistenceRoundtrip|TestIsPasscodeRequiredErr'
```
