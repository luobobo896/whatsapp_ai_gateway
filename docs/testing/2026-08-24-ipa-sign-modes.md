# 2026-08-24 IPA 个人 / 企业同一套出包

- 日期：2026-08-24
- 目的：`package-wda-ipa.sh` 同时支持个人付费账号（绑 UDID）和企业 In-House（不绑设备）

## 规则

| SIGN_MODE | 签名 | 描述文件 | 新机 |
|---|---|---|---|
| `personal`（默认 / auto 无文件时） | 自动，`-allowProvisioningUpdates` | 列出 `ProvisionedDevices` | 先登记 UDID 再出包 |
| `enterprise` | 手工 `iPhone Distribution` | 必须 `ProvisionsAllDevices=true` 且设备数为 0 | 不用登记 |

`auto`：若传入 `.mobileprovision` 或已有 Runner.app，跟文件走；发行证书名但没有描述文件则失败，避免把 Ad Hoc 当成企业。

已有 Runner.app 与目标模式不一致时重编；`SKIP_BUILD=1` 则失败。打完用 `ipa-inspect -require` 再验一次。

## 验证

```bash
go test ./internal/ipasign ./cmd/ipa-inspect -count=1
go run ./cmd/ipa-inspect -ipa dist/wda.ipa -field mode
```

本机现有 `dist/wda.ipa` 应为 `personal`（开发描述文件，有设备列表）。企业路径在没有 In-House 证书的机器上只跑套件，不真机出包。
