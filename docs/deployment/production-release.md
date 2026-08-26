# 生产发布说明（WDA Farm Gateway）

> 用途：把本次发布包提供给用户前的**检查、打包、分发、安装、验收**全流程。
> 版本：功能以 commit **`1f8b644`** 为准。
> 相关：`macos-dmg打包操作手册`、`macos-dmg替换操作手册`、`windows-exe打包`、`README`、`docs/design/wda-end-to-end-flow.md`。

---

## 1. 发布前检查清单（本次已执行）

| 检查 | 结果 |
|---|---|
| `go test ./...` | 291 passed / 9 packages |
| `go build ./...` / `go vet ./...` | 通过 |
| 闭环 e2e（USB 通道）`TestAutonomyEndToEndSend` | 通过（自主群发→发送→落库→回填） |
| 任务实时通知 `TestEventHubBroadcastOverWS` | 通过（WSS 广播） |
| 跨天去重 / 探活回退 / 模板变量 | 用例通过 |
| Mac 新包（`.app`/`.dmg`） | 已构建，含自主群发/任务通知 |
| Windows 新包（`dist/windows-amd64/`） | 已重新交叉编译，含自主群发/任务通知 |
| `wda.ipa` 签名 | **个人开发证书**（TeamID `A3JP3VUZ78`，`get-task-allow=1`），仅覆盖**已登记的 5 台 UDID** |

> ⚠ 核心可用性结论：**`wda.ipa` 只能装进那 5 台已登记 UDID 的 iPhone**。用户新手机需：
> (a) 提供 UDID，在个人开发者账号登记后用 `scripts/package-wda-ipa.sh` 重签；或
> (b) 升级为**企业开发者证书**（`SIGN_MODE=enterprise`）重签，免逐台登记。
> 这两者在发布前必须先确认用户手机是否在这 5 台内，否则装机后无法激活。

---

## 2. 交付物清单（放在同一发布目录，便于分发）

| 文件 | 平台 | 说明 |
|---|---|---|
| `WDAFarmGateway-279fc23-arm64.dmg` | macOS(arm64) | 桌面应用安装包（含网关+壳）；**ad-hoc 签名，首次打开需右键→打开** |
| `dist/windows-amd64/`（打包成 zip） | Windows | 含 `gateway.exe`、`WDAFarmGateway.exe`、`wda-probe.exe`、`bin/`、`wda.ipa`、`使用说明.txt` |
| `tools/wda.ipa`（或 `dist/windows-amd64/wda.ipa`） | iPhone | WDA 控制器（已签，仅限登记 UDID） |
| `docs/deployment/production-release.md`（本文件） | — | 给发布者的说明 |
| `使用说明.txt`（已随包） | 用户侧 | 快速安装/启动指引 |

> `dist/latest.json` 是**本机构建记录**（含绝对路径/哈希），不是分发元数据；不要把仓库内构建目录直接发给用户，只发上述清单。

### 校验和（供用户核对）

```bash
shasum -a 256 WDAFarmGateway-279fc23-arm64.dmg
# dmg  sha256  011d238054cc09c63ba26f2d7b81938c1d45a02d23eb1e0c37e95d5abbbfdcdd
shasum -a 256 dist/windows-amd64/gateway.exe
# win gateway sha256  5d659ccf1b20f9ae0c97eb3093af9430105d5f3b38c30c120a7631142a1b974c
```

---

## 3. 怎么提供给用户（分发方式）

推荐**打包成一个 zip** 上传到共享盘 / 网盘 / 对象存储，并附一份"使用说明"：

```bash
mkdir -p release/wda-farm-gateway-1f8b644
cp WDAFarmGateway-279fc23-arm64.dmg release/wda-farm-gateway-1f8b644/
cp -R dist/windows-amd64 release/wda-farm-gateway-1f8b644/windows-amd64/
cp tools/wda.ipa release/wda-farm-gateway-1f8b644/
cp docs/deployment/production-release.md release/wda-farm-gateway-1f8b644/
# 打成 zip 后把 zip + 校验和发给对应平台用户；只给对应平台文件即可（Mac 只发 dmg，Windows 只发 windows-amd64/）。
```

给用户时明确：**每一位用户只拿自己平台的那一份**，不要混用；iPhone 装机需对应 UDID 在登记列表。

---

## 4. 用户安装与启用步骤

### 4.1 macOS 用户（arm64 主机）

1. 打开 `WDAFarmGateway-*-arm64.dmg`，把 `WDAFarmGateway.app` 拖到「应用程序」。
2. **首次打开右键→打开**（ad-hoc 未公证，直接双击可能提示"无法验证开发者"）。
3. 打开后浏览器自动打开 `http://127.0.0.1:8300/`（或在应用内登录）。
4. 插上 iPhone（USB），按提示完成「首次授权」（**必须插 USB + 已设锁屏密码，场内统一 `0000`**）。
5. iPhone 上信任开发者、开启**开发者模式**（iOS 16+）；点激活（USB 或 Network）。

### 4.2 Windows 用户

1. 解压 `windows-amd64/`，双击 `WDAFarmGateway.exe`（托盘+管理窗）或 `gateway.exe`（浏览器管理页）。
2. 安装 Apple Devices / iTunes；插 iPhone 并点「信任」。
3. 首次授权同样**只走 USB**、输入锁屏密码 `0000`；普通权限即可发消息；**仅当要开 easytier 虚拟网卡时才右键以管理员运行**。
4. SmartScreen/杀毒可能提示"未知发布者"：选择"仍要运行"（未签名 exe，需确认来源）。
5. 首次可能被 Windows 防火墙拦截 8300 端口：允许。

### 4.3 iPhone / WDA 控制器（关键）

- 支持：iPhone 7 及以上，**iOS 15~当前最新**；iOS 16+ 必须开**开发者模式**。
- 装 `wda.ipa`（通过桌面端"激活"自动安装，或手动装）。
- **装机前提**：该 UDID 必须在个人签名 `embedded.mobileprovision` 的 `ProvisionedDevices` 内（当前 5 台）。不在 → 走签名门槛处理（见 §1）。
- 首次激活流程：USB 插线 + 锁屏密码 → 点「首次授权」→ 信任开发者 → 激活。

---

## 5. 用户侧验收清单（装好后自查）

1. 管理页 `http://127.0.0.1:8300/` 打开，**云通道**显示"已连接"。
2. **设备**列表出现 iPhone（online/busy），点「健康」探活通过。
3. 在平台下发一条测试任务；管理页出现🔔铃铛 + 任务实况转圈，结束后有 toast 统计（成功/失败）。
4. 发送一条消息，`/api/metrics` 今日成功 +1；`发送明细`能看到记录。
5. 掉线/拔线后按文档验证自动恢复（Network/看护）。

---

## 6. 回滚与降级

- Mac：保留旧 `.app` 备份，异常时用 `macos-dmg替换操作手册.md` 的"回滚"步骤。
- Windows：保留上一版 `windows-amd64/`，覆盖回旧 exe 即可。
- iPhone：若 wda.ipa 激活异常，用重新签名（登记 UDID / 企业签名）的 ipa 重装。

---

## 7. 已知风险（诚实声明）

- **不公证 / 非企业签名**：Mac 首次右键打开、Windows 未签名 exe 可能被系统/杀毒拦，属预期，需用户确认来源。
- **个人签名仅 5 台**：新设备装机前必须登记或企业签名，**这是当前最大限制**。
- 真机端到端（真实 UI 选择器/键盘/主题页）建议在有真机的机器上先跑一轮再交付；网关侧闭环已用 mock WDA 覆盖协议层。
