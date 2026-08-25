# Network 激活：iPhone 锁屏密码框

日期：2026-08-25

> 2026-08-25 更正：写 `EnableWifiConnections` / `EnableWifiDebugging` 已改成 **USB 首次授权**，不再由 Network 激活去写。见 [usb-network-activate.md](../design/usb-network-activate.md) 与 [2026-08-25-first-connect-wifi-auth.md](2026-08-25-first-connect-wifi-auth.md)。下文保留当时的现象记录。

## 现象

点授权或写 `EnableWifiDebugging=true` 时，iPhone 会弹出锁屏密码框。在**手机上**输入锁屏密码后，无线调试开关才能写成 true，随后 usbmux 才可能出现 Network 行，WDA 才能走无线通道。

密码只在手机上输入。网关、网页、日志、配置都不得接收或保存锁屏密码。

## 产品行为

1. 点「Network 激活」立刻提示：看 iPhone，若弹出密码框请在手机上输入。
2. `wifi-lockdown` 先读开关，未开则写入；遇到 `PasscodeRequired` 等待最多 60 秒并重试。
3. 用户在等待窗口内输入成功 → 继续 `wifi-runwda`。
4. 超时仍未输入，或手机还没设置锁屏密码 → 接口返回 `need_passcode=true` 和说明，不假装激活成功。

## 复验

```bash
go test ./cmd/wifi-lockdown ./internal/gateway -count=1 -timeout 90s \
  -run 'TestIsPasscodeRequiredErr|TestPasscodeTimeoutErrToken|TestIsDevicePasscodeOutput|TestEnableWifiLockdownPasscodeError|TestEnableWifiLockdownMissingBinaryIsNoop|TestProtocolCmdIOS15NetworkUsesWifiRunwda'
```

不要在命令、测试或文档里写入真实锁屏密码。
