# 个人开发者账号：新设备 UDID 自动签名分发 wda.ipa

> 目标：在**未申请到企业开发者账号前**，用**个人开发者账号**支撑"用户新手机接入 → 自动出一份能装的新 `wda.ipa` → 给用户更新"。
> 企业账号下发后，该流程关闭，改为"企业包免登记"。

---

## 0. 业务闭环

```
用户新手机插网关
   → 网关读 UDID（idevice_id -l / USBUDIDs）
   → 上报云平台「设备 UDID 表」
   → 你在出包 Mac 上「从表拉 UDID 列表」
   → 自动重签（个人：fastlane 生成含这些 UDID 的 profile → 重签）
   → 上传生产服务器「安装包目录」
   → 用户点「更新安装包」→ 网关下载/替换 wda.ipa → 激活
```

**账号类型分流**：

| 账号 | `SIGN_MODE` | 是否登记 UDID | 是否需要本流程 |
|---|---|---|---|
| 个人开发者 | `personal` | **要**（每台登记+重签） | 需要自动重签脚本 |
| 企业开发者 | `enterprise` | 不需要（`ProvisionsAllDevices`） | **不需要**，一份总包全设备可装 |

> 云平台"账号类型"可配置：登录的是个人 → 走 `personal` + 自动重签；换成企业 → 走 `enterprise`，跳过登记与重签。

---

## 1. 硬性依赖（没有就无法"自动"）

自动把**新 UDID 注册进 Apple 后台并生成 profile**，必须是 Apple 官方可编程通道：

| 依赖 | 用途 | 本机现状 |
|---|---|---|
| **fastlane**（`brew install fastlane`）+ `sigh` | `sigh adhoc --udids <列表>` 注册设备并生成含这些 UDID 的 `.mobileprovision` | ❌ 未安装 |
| Apple 开发者账号（Apple ID 或 App Store Connect **API key**） | `sigh` 登录/授权 | 需你提供（API key 首选，CI 友好） |
| 个人开发证书私钥（`Apple Development`） | `codesign` 重签 | 有（`D6019E6622... / V5SSJCVBLF`） |
| **Team 对齐** | 证书 Team 与 ipa 的 `TeamIdentifier` 必须一致 | ⚠ 当前证书 Team=`V5SSJCVBLF`，ipa profile Team=`A3JP3VUZ78` **不一致** |
| 生产服务器目录 | 上传新 ipa（scp/S3/路径） | 需你提供地址/凭据 |
| 云平台"UDID 表"取数 | 拉到 UDID 列表 | 需云平台提供一个接口/文件（本仓库仅留 URL 占位） |

> 说明：`xcodebuild -allowProvisioningUpdates` 只对**已在账号设备列表**的 UDID 更新 profile，**不会自动登记新 UDID**。所以"插新机自动出包"必须走 fastlane/API。

---

## 2. 待你确认 / 提供

1. 能否**安装 fastlane** + 提供 **App Store Connect API key**（或 Apple ID 登录）？
2. **生产服务器**目录怎么访问（scp 主机/路径？S3？）？
3. **云平台 UDID 表**的取数入口（返回 JSON 数组的 URL，或一个文件路径）？
4. 证书/Team 是否可统一到 `A3JP3VUZ78`（或把 ipa 的 team 换成证书的 team）？

---

## 3. 本仓库已实现的编排骨架

`scripts/resign-wda-for-udids.sh`：

- `--mode personal|enterprise`：账号类型分流；`enterprise` 直接走 `package-wda-ipa.sh SIGN_MODE=enterprise`（免登记）。
- `--udids <列表|文件|URL>`：从参数 / 文件 / 云平台 URL 拉 UDID。
- 前置自检：检测 `fastlane`、`Apple Development` 证书、Team 对齐、UDID 数量，缺一明确报错。
- `personal`：有 fastlane 且 UDID 非空 → `fastlane sigh` 生成 profile → 传入 `package-wda-ipa.sh PROFILE=... SIGN_MODE=personal`；否则报"无法自动登记新 UDID"。
- 输出 `dist/wda-<mode>-<short>.ipa`；可选 `--upload <dst>` 上传。

> ⚠ 现在本机缺 fastlane + Team 不一致，所以 `--mode personal --udids <新机>` 会**自检失败并给出可操作提示**——这是诚实行为：不假装能跑通。

---

## 4. 后续（拿到企业账号）

把平台账号类型设为企业，去掉上面 1、2、3 依赖，`--mode enterprise` 出一份内部总包即可；用户端不再需要逐台登记/重签，点击更新只是"替换企业包"。

## 5. 云平台侧联动（whatsapp_ai，已实现）

信息中心在云平台、由云平台下发。已在 `whatsapp_ai`（Gin + pgx）实现：

### 5.1 云平台接口

| 接口 | 说明 |
|---|---|
| `GET /api/wda/udids` | 取平台全部非空 UDID（`mobile_devices.udid` 去重）→ 供签名脚本 |
| `GET /api/wda/package` | 当前发布的 WDA 包（version/sign_mode/sha256） |
| `POST /api/wda/package` | multipart 上传新 `.ipa` + `sign_mode` → 存 `wda_packages/` → 落 `wda_packages` 表 → 向在线网关广播 `wda:config` |
| `GET /api/wda/package/download` | 下载当前 `.ipa` |

门店/管理员端：`RegisterWDAPackageRoutes(api.Group("/wda", RequireActiveTenant()), st, gatewayHub)`（在 `cmd/server/main.go` 注册）。

### 5.2 下发协议

新增下行消息：**`wda:config`**（`GatewayHub.BroadcastWDAPackage`），载荷 `GatewayWDAPackage{version, sign_mode, download, sha256}`。上传后即广播；网关离线会在重连后由管理端重发或重传。

### 5.3 网关联动（whatsapp_ai_gateway，已实现）

网关 `CloudLoop` 收到 `wda:config` → `ApplyWdaPackage`：
- 从 `Cloud.WSURL` 推导下载地址 → 下载 → 校验 sha256 → **原子替换 `<state>/wda.ipa`**（先写临时再 rename）。
- 记录 `wda_package_version/sha256/sign_mode` 到 `gateway.db`，并推送管理页 `wda:update` 事件（前端响铃/日志可见）。
- 之后用户重新激活/网关安装 Runner 时即用新包。

### 5.4 完整用户闭环

```
新手机插网关
  → UDID 自动落 mobile_devices.udid（已有）
你（出包 Mac）：curl -s https://hk.hsddns.com/api/wda/udids   # 拉全部已登记 UDID
  → fastlane register_and_sign（已跑通）→ 生成新 wda.ipa
  → curl -F file=@new.ipa -F sign_mode=personal -H "Authorization: Bearer <token>" \
       POST https://hk.hsddns.com/api/wda/package               # 上传即广播 wda:config
各网关：自动下载替换 wda.ipa → 下次激活用新包；用户视为"安装包已更新"
```

### 5.5 已验证

| 项 | 证据 |
|---|---|
| 云平台 build/vet | `whatsapp_ai` `go build ./...` / `go vet ./internal/...` 通过 |
| 网关接收替换 | `internal/gateway/wda_package_test.go`（httptest 下载→校验→替换→记录版本）通过 |
| 认证/生成 profile | fastlane `cert+sigh development` 成功（`UPRYX5Q2RB` / `com.wda.WebRunner Development`） |
| 注册新 UDID | fastlane `register_device`（API key）跑通 |
