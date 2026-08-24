# 2026-08-24 激活时自动安装描述文件并信任开发者

- 日期：2026-08-24
- 目的：企业 / 个人同一条激活路径，不再手工去「VPN 与设备管理」装证书

## 行为

1. 装 IPA 前：从 `wda.ipa` 抽出 `embedded.mobileprovision`，用 `ideviceprovision install`（失败再 `ios profile add`）装到手机。已有相同 UUID 则跳过。
2. WDA `/status` ready 后：点权限按钮，并打开设置里的设备管理页，点「信任 / Trust」。个人「开发者 App」和企业「企业级 App」是同一页。

点不到按钮不判激活失败。iOS 在未监督设备上仍可能弹出一次锁屏密码，这是系统限制。

## 验证

```bash
go test ./internal/ipasign ./internal/wda ./internal/gateway -count=1
```

覆盖：从假 IPA 抽出描述文件、`ideviceprovision`/`ios profile add` 参数、信任按钮文案（含「不信任」不点）。
