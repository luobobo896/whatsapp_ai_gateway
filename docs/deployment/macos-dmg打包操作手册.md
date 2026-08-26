# macOS DMG 打包操作手册

> 对象：需要在**任何一台新 Mac** 上从零打包 WDA Farm Gateway 的人。
> 脚本：`scripts/build-dmg.sh`（约 200 行，7 步流水线，白名单制组装）。
> 设计决策与选型背景见 [2026-08-16-macos-dmg桌面客户端打包方案.md](./2026-08-16-macos-dmg桌面客户端打包方案.md)。
> 用新包**替换已运行的应用**见 [macos-dmg替换操作手册.md](./macos-dmg替换操作手册.md)。

---

## 1. 一分钟结论

```bash
sh scripts/build-dmg.sh
```

- **成功的唯一标志**：仓库根出现 `WDAFarmGateway-<git短hash>-arm64.dmg`，且日志末尾有 `✅ 完成` 和 `✓ 无敏感数据泄漏`。
- **最常见的失败**：在只克隆了本仓库的机器上打包，脚本在第 3 步报 `✗ WDA 工程不存在` 后退出。此时 `build/` 目录下的 `gateway`、`WDAFarmGateway` 是**无扩展名的裸二进制中间产物，不是应用程序**——不要拿去用。
- 本仓库 git 里**不含** 3 样打包必需品，见下节。

## 2. 前置条件（新 Mac 从零准备）

### 2.1 环境依赖

| 依赖 | 用途 | 缺失后果 |
|---|---|---|
| Xcode（完整版，装完打开一次） | Swift 壳构建；客户侧激活 WDA 也依赖它 | 第 2 步 swift build 失败 |
| Go（版本见 `go.mod`） | 编译网关主程序 | 第 1 步失败 |
| Homebrew + `brew install libimobiledevice` | 随包拷贝 `iproxy`/`ideviceinfo`/`idevice_id` | **不报错中断**，但打出的包缺 USB 隧道三件套（第 4 步只有 ⚠ 警告），功能残缺 |

### 2.2 仓库外依赖（git 拉不到的，重点）

| 缺失项 | 原因 | 缺失后果 |
|---|---|---|
| **WDA 源码工程**：`whatsapp_ai_ios` 仓库（独立 git 仓库，本仓库不含它的任何文件） | 打包要把它 rsync 进 bundle，激活时由 xcodebuild 现场编译装进 iPhone | 脚本第 3 步 `✗ WDA 工程不存在` 直接 `exit 1`，**不会有 DMG 产出** |
| **easytier 二进制**（`tools/easytier/easytier-core`、`easytier-cli`） | 被 `.gitignore` 排除（29MB 二进制不入库） | 第 3 步 `cp` 失败退出 |

版本与下载地址（与 `scripts/install.sh` 一致）：EasyTier `v2.6.4` macOS aarch64。

### 2.3 机型/系统兼容与 iOS 15 老设备的 DeveloperDiskImage

- **支持范围**：iPhone 7 及以上机型（arm64 构建，兼容 arm64/arm64e 设备）、iOS 15 及以上系统。
- **iOS 16+ 前置**：客户设备需在「设置 → 隐私与安全性」开启「开发者模式」。
- **iOS 15/16 老设备激活**：依赖 `DeveloperDiskImage.dmg`（DDI）挂载。网关在激活前会自动从
  本机 Xcode 复制匹配版本的 DDI 到 `~/Library/Developer/Xcode/iOS DeviceSupport/<型号> <版本> (<构建>/`
  （幂等，日志可见 `EnsureDeviceSupportDDI: 已补齐`）。要求客户机 Xcode 自带 15.x/16.x 镜像
  （Xcode 16.x 自带 15.0/15.2/15.4/15.5，够用）；若某台客户机 Xcode 过新缺少 15.x，需手动放置
  对应 DDI 或换用 Xcode 16.x。
- **UDID 格式差异**：老机型 40 位 hex（iOS ≤15）用无前缀 `id=` destination，新款连字符格式
  （iOS 16+）用 `platform=iOS,id=`+大写，网关自动区分，无需人工干预。

## 3. 从零打包步骤（新 Mac 完整版）

```bash
# 0) 环境：Xcode、Go、Homebrew，然后：
brew install libimobiledevice

# 1) 两个仓库克隆成兄弟目录（脚本默认找 ../whatsapp_ai_ios/WhatsAppDeviceAgent）
cd ~/work                            # 任意父目录
git clone https://github.com/luobobo896/whatsapp_ai_gateway.git
git clone https://github.com/luobobo896/whatsapp_ai_ios.git

# 2) 补 easytier 二进制（.gitignore 排除，需手动下载）
cd whatsapp_ai_gateway
curl -fsSL -o /tmp/et.zip \
  https://github.com/EasyTier/EasyTier/releases/download/v2.6.4/easytier-macos-aarch64-v2.6.4.zip
unzip -o /tmp/et.zip -d /tmp/et
cp /tmp/et/easytier-macos-aarch64/easytier-core /tmp/et/easytier-macos-aarch64/easytier-cli tools/easytier/
chmod +x tools/easytier/*

# 3) 打包
sh scripts/build-dmg.sh
# WDA 工程不在默认兄弟目录时：
WDA_PROJECT_DIR=/path/to/WhatsAppDeviceAgent sh scripts/build-dmg.sh

# 4) 验收：仓库根出现 WDAFarmGateway-<hash>-arm64.dmg，日志末尾「✅ 完成」「✓ 无敏感数据泄漏」
```

常用环境变量：

| 变量 | 作用 |
|---|---|
| `WDA_PROJECT_DIR` | 覆盖 WDA 工程路径（默认 `../whatsapp_ai_ios/WhatsAppDeviceAgent`） |
| `SKIP_TESTS=1` | 跳过打包前的 go test |
| `SIGN_IDENTITY` | 指定签名身份；缺省自动找 "Developer ID Application"，找不到则 ad-hoc |
| `NOTARY_PROFILE` | 设置后对 DMG 公证 + staple；未设则跳过 |

## 4. 脚本七步流程

| 步骤 | 做什么 |
|---|---|
| [1/7] Go 测试与构建 | `go test ./...` 全绿才继续；`go build -trimpath -ldflags "-s -w"` 编译网关主程序 |
| [2/7] Swift 壳构建 | `desktop/` 下 `swift build -c release`，产出菜单栏壳（拉起 gateway 子进程、注入 PATH 与 dylib 环境变量） |
| [3/7] 组装 .app | **白名单制**：两个二进制、Info.plist（版本号写 `git describe`）、图标、Web 管理页、WDA 源码工程（剔除 .git/DerivedData）、easytier 二进制与授权脚本 |
| [4/7] libimobiledevice 随包 | 从 Homebrew 拷 `iproxy`/`ideviceinfo`/`idevice_id`（**原样不改 Mach-O**——改了 dyld4 会启动死循环）+ 递归收集依赖闭包 6 个 dylib 到 `lib/`（含短名软链），运行时由壳注入 `DYLD_FALLBACK_LIBRARY_PATH` 解析 |
| [5/7] 签名 | 有 Developer ID 证书则签（hardened runtime + timestamp）；否则 ad-hoc（客户首次打开需右键→打开） |
| [6/7] 安装说明 | 生成 `安装说明.txt` 随 DMG 分发（Xcode 依赖、首次打开、系统弹窗、登录激活、排障） |
| [7/7] DMG + 敏感数据终检 | staging（.app + 说明 + Applications 软链）压成 UDZO；**挂载 DMG 终检**：grep 真实云 token / LLM api_key、确认 `devices.json` 与 `data/` 不在包内，任一命中即报 ✗ |

## 5. 包内容清单（以 ec42553 实测）

.app 共约 51M，DMG 压缩后 24M：

```
WDAFarmGateway.app/Contents/
├── Info.plist                        版本 = git describe
├── _CodeSignature/
├── MacOS/                            12M
│   ├── gateway                       11M   Go 网关主程序（看护/USB隧道/云通道/Web）
│   └── WDAFarmGateway                166K  Swift 菜单栏壳
└── Resources/                        39M
    ├── AppIcon.icns                  914K
    ├── WhatsAppDeviceAgent/          2.9M  397 个文件：WDA 源码工程
    │     （WebDriverAgentLib/Runner/xcodeproj…，激活时现场编译装进 iPhone）
    ├── bin/                          libimobiledevice 三件套
    │   ├── iproxy                          USB 隧道端口转发（必需）
    │   ├── ideviceinfo                     查序列号/配对信息
    │   └── idevice_id                      usbmux 设备枚举（USB 发现主源）
    ├── lib/                          6 个 dylib（bin 的依赖闭包）
    │   ├── libcrypto.3.dylib         4.7M
    │   ├── libssl.3.dylib            885K
    │   ├── libimobiledevice-1.0.6 / -glue-1.0.0
    │   └── libplist-2.0.4 / libusbmuxd-2.0.7
    ├── static/index.html             Web 管理页（8300 端口）
    ├── scripts/
    │   ├── install.sh                一键部署：装 easytier + sudoers 免密授权
    │   └── setup-easytier-sudo.sh
    └── tools/easytier/
        ├── easytier-core             组网后备通道（默认关闭）
        └── easytier-cli
```

DMG 卷内除 `.app` 外：`安装说明.txt`、`Applications` 文件夹软链（拖拽安装用）。

**明确不入包**（终检强制）：

- 仓库根 `devices.json`（真实云 token、LLM api_key）；
- `data/` 本地运行数据；
- `.git/`、WDA 工程的 DerivedData、评审缓存。

## 6. 常见问题

### 打出来的"不是应用程序"

**现象**：找不到 DMG，或拿到的是 `build/gateway`、`build/WDAFarmGateway` 这类无图标无扩展名的文件。

**根因**：脚本中途 `exit 1`（99% 是 §2.2 的 WDA 工程或 easytier 缺失），`build/` 下的裸二进制只是第 1、2 步的中间产物。

**自查**：

1. 重跑 `sh scripts/build-dmg.sh`，看停在哪一步、报什么错；
2. `ls -d ../whatsapp_ai_ios/WhatsAppDeviceAgent` 是否存在；
3. `ls tools/easytier/` 里是否有两个二进制；
4. 完整成功日志应依次出现 7 个 `▶` 步骤头和最后的 `✅ 完成`。

### 包里没有 USB 隧道功能

打包日志第 4 步出现 `⚠ 本机无 iproxy` → 忘了 `brew install libimobiledevice`。这个缺失**不会中断打包**，最易漏。

### ad-hoc 签名的包客户打不开

正常：无 Developer ID 证书时是 ad-hoc 签名，客户首次打开需 右键→打开（或 系统设置→隐私与安全性→仍要打开）。有证书后按 §3 的 `SIGN_IDENTITY`/`NOTARY_PROFILE` 走签名公证。

### 只想重新打 Go 侧改动（跳过测试）

```bash
SKIP_TESTS=1 sh scripts/build-dmg.sh
```
