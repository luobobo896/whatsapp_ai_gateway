# 2026-08-24 USB 最短激活

- 日期：2026-08-24
- 目的：点激活尽快出现 Automation Running；Agent 已去掉扫码注册，只点网络等权限按钮

## 最短路径

USB 已插、Runner 已装：

`读缓存版本 →（仅 17+ 起隧道）→ ios runwda → 最多 20s 探 /status（400ms 间隔）→ 点允许/本地网络`

不再做：

- `wifi-lockdown` + 固定 2s
- `wifi-runwda -wait-network` 45s
- 每次 `ios image auto`
- 扫码 / 注册按钮

## 验证

```bash
go test ./internal/gateway ./internal/wda -count=1
```

2026-08-24：通过。`TestProtocolCmdUSBPrefersRunwdaNotWifiWrapper`、`TestIsPermissionAllowLabel` 覆盖「不走无线包装器、不点扫码注册」。
