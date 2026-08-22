# 2026-08-22 Windows 出门包

## 评审结论

没有会挡住出包/激活的缺陷。Windows 当晚应先验 **USB 发现 → 装 IPA → WDA ready**，再测发送。

仍存在、但不挡出包：

| 项 | 说明 | Windows 影响 |
|---|---|---|
| 任务 `sent/total` 按明细条数 | 留空是 1 条；人数在明细「说明 / 收件人」 | 看明细，不要只看 1/1 |
| 本机已装的 Mac App | 仍是旧网关，不含今晚发送修复 | Windows 用新 `gateway.exe` |
| `ios.exe` / `tidevice` / `iproxy` | 不随包 | Windows 要另装 |
| applist 失败且没 IPA | 激活会先试拉起，错误偏晚 | 把 `wda.ipa` 放在 exe 同目录即可 |
| 幽灵 UDID `00008120` | 描述文件里有，USB 列表偶发 | 忽略，用 `4886579a` / `59524996` |
| iOS 17+ wintun | 未核验 | iPhone 7 Plus 是 15.8.8，不需要 |

## 产物（2026-08-22 20:18）

| 文件 | 用途 |
|---|---|
| `dist/wda.ipa`（5.8M） | 已签名 Runner，bundle `com.wda.WebRunner.xctrunner` |
| `dist/windows-amd64/gateway.exe` | 含今晚发送修复（人数、回聊天列表、进页再发） |
| `dist/windows-amd64/wda.ipa` | 同上 IPA，已拷进分发目录 |
| `dist/windows-amd64/wda-probe.exe` | 可选探针 |

描述文件有效期到 2027-08-14，已含当前两台 USB 机：`59524996…`、`4886579a…`。

## 回家带什么

`dist/windows-amd64/` 整个目录 + 已信任开发者证书的 iPhone + 线。不要拷 `gateway.db` / 云凭证。

Windows 上把 `wda.ipa` 放在 `gateway.exe` 同一目录（或 `-state`），装 Apple Devices/`ios.exe` 后点激活。步骤见 `docs/deployment/windows-night-runbook.md`。
