# macOS 已装应用替换操作手册

> 对象：在**已经装过 `/Applications/WDAFarmGateway.app` 的这台 Mac** 上，用新构建的包替换旧包。
> 顺序强调：**替换前必须停止应用，替换后再启动**。
> 打包（产出 `.app`/`.dmg`）见 [macos-dmg打包操作手册](./macos-dmg打包操作手册.md)；本手册只讲**替换运行中的应用**。
> 适用平台：macOS arm64（`scripts/build-dmg.sh` 仅构建 arm64）。

---

## 0. 一分钟结论

```bash
# 仓库根 `<repo>` 执行。先停，再建，再换，最后启动。
osascript -e 'tell application "WDAFarmGateway" to quit' && sleep 2
sh scripts/build-dmg.sh
TS=$(date +%Y%m%d-%H%M%S)
mv /Applications/WDAFarmGateway.app /Applications/WDAFarmGateway.app.bak-$TS
ditto build/WDAFarmGateway.app /Applications/WDAFarmGateway.app
xattr -dr com.apple.quarantine /Applications/WDAFarmGateway.app 2>/dev/null || true
open /Applications/WDAFarmGateway.app
```

---

## 1. 前置条件

| 项 | 要求 | 缺失后果 |
|---|---|---|
| 工具链 | `go`（版本见 `go.mod`）、Xcode+`swift`、`xcodebuild` | 第 3 步 `swift build` / `go build` 失败 |
| WDA 工程 | `../whatsapp_ai_ios/WhatsAppDeviceAgent` 存在 | 构建脚本第 3 步 `✗ WDA 工程不存在` 退出 |
| `/Applications` 可写 | 当前用户对 `/Applications` 有写权限（本仓库场景已验证属主为 `hanson`） | 无法覆盖 app |
| 运行中实例 | 允许存在，但**必须本次停止** | macOS 覆盖二进制可成功但会留旧进程/状态混乱；Windows 会因文件占用直接失败 |

> 若只想要新 `gateway` 逻辑而不重打桌面壳，可退而求其次：只重建 `gateway` 二进制 + 拷贝新 `static/index.html`
> 进 app 对应目录（见 §3 备注），但**不推荐**作为正式替换，因为本地改动可能涉及 Shell/Info.plist/资源。

---

## 2. 停止当前应用

```bash
# 优雅退出桌面壳（会连带退出其托管的 gateway 主进程）
osascript -e 'tell application "WDAFarmGateway" to quit'
sleep 2

# 确认停止；若仍有残留，用 kill 显式停掉主进程
pgrep -fl "WDAFarmGateway|Contents/MacOS/gateway" || echo "已全部停止"

# 清理可能残留的 WDA 子进程（ios forward / wifi-runwda / runwda）——仅匹配 WDA 相关，勿误伤其它
pkill -f "ios forward|wifi-runwda|runwda" 2>/dev/null || true
```

> ⚠ 停止会**中断当前在跑的任务与手机 WDA**。恢复需新应用启动后重新登录/等待看护或手动激活。

---

## 3. 构建新包

```bash
# 可选：跳 go test 提速（SKIP_TESTS=1）。推荐先 go test 通过再构建。
sh scripts/build-dmg.sh
```

**成功标志**：
- `build/WDAFarmGateway.app` 出现；
- 仓库根出现 `WDAFarmGateway-<git短hash>-arm64.dmg`；
- 日志末尾有 `✅ 完成` 与 `✓ 无敏感数据泄漏`。

若失败，参见 [macos-dmg打包操作手册](./macos-dmg打包操作手册.md) 的"失败排查"。

---

## 4. 替换（备份 + 覆盖）

```bash
TS=$(date +%Y%m%d-%H%M%S)
# 1) 备份旧 app（可回滚），不要直接覆盖
mv /Applications/WDAFarmGateway.app "/Applications/WDAFarmGateway.app.bak-$TS"
# 2) 用 ditto 拷贝新 app（保留扩展属性/资源 fork，比 cp -R 稳）
ditto build/WDAFarmGateway.app /Applications/WDAFarmGateway.app
# 3) 确认新文件确实替换（时间应为刚刚构建）
ls -la /Applications/WDAFarmGateway.app/Contents/MacOS/gateway \
      /Applications/WDAFarmGateway.app/Contents/Resources/static/index.html
```

> `state` 目录（`~/Library/Application Support/WDAFarmGateway`：配置、`results.db`、`data/`）**不在 app bundle 内**，
> 替换 app 不影响数据；已装到手机的 WDA/Runner、云凭证、设备配置全部保留。

---

## 5. 启动

```bash
# ad-hoc 签名（无 Developer ID）可能被 Gatekeeper 拦；先清隔离标记
xattr -dr com.apple.quarantine /Applications/WDAFarmGateway.app 2>/dev/null || true
open /Applications/WDAFarmGateway.app
```

若仍被拦：Finder 右键 app → 打开，或 `spctl --add /Applications/WDAFarmGateway.app`。

---

## 6. 验证清单

| 验证 | 命令 | 期望 |
|---|---|---|
| 进程 | `pgrep -fl "WDAFarmGateway\|Contents/MacOS/gateway"` | 出现壳 + `gateway` 两行 |
| 服务 | `curl -s http://127.0.0.1:8300/api/session` | 返回 `{"authenticated":...,...}`（未登录也正常） |
| 新接口在 | `curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:8300/api/autonomy/status` | `401`（=路由存在且需登录）；`404`=路由缺失 |
| 含新逻辑 | `strings /Applications/WDAFarmGateway.app/Contents/MacOS/gateway | grep -c "task:new\|autonomy"` | `>=1` |
| 前端更新 | 对比 `index.html` 时间（应为本轮构建时间） | 新 |
| WDA 恢复 | `pgrep -fl "ios forward\|wifi-runwda\|runwda"` | 登录后约 30s 看护拉起，或手动激活后出现 |

---

## 7. 回滚

```bash
# 把疑似异常的新包挪走，恢复备份
mv /Applications/WDAFarmGateway.app /Applications/WDAFarmGateway.app.broken-<ts>
mv "/Applications/WDAFarmGateway.app.bak-<ts>" /Applications/WDAFarmGateway.app
open /Applications/WDAFarmGateway.app
```

确认新包稳定后，再删除 `.bak-*` 备份。

---

## 8. 边界与注意

- 本手册针对 `/Applications/WDAFarmGateway.app`。若你在开发时直接跑仓库根 `./gateway`（裸二进制，非 app），替换方式不同（那是 `go run`/`go build -o gateway` + `-state`），不走本手册。
- **Windows 替换**：用 `dist/windows-<arch>/gateway.exe`（桌面壳 `WDAFarmGateway.exe`），停止方式不同（托盘退出/任务管理器结束），见 [windows-desktop-shell.md](./windows-desktop-shell.md)；**不走本手册**。
- 每次 `build-dmg.sh` 会清理仓库根旧的 `WDAFarmGateway-*-arm64.dmg`（属脚本正常行为），不影响已安装 app。
- 替换不会自动迁移/清空 `state` 数据；若此前旧包已登录下发凭证，新包起步后沿用。

---

## 9. 本次执行记录（2026-08-26）

操作：停止 59607/59646（含 WDA 子进程）→ 构建 `279fc23`（`WDAFarmGateway-279fc23-arm64.dmg`，57M，ad-hoc）→
备份 `.bak-20260826-112449` → `ditto` 替换 → 启动 52997/53035，监听 `8300`。

验证：`/api/session` 正常；`/api/autonomy/status` 返回 401；`strings` 命中 `task:new`/`autonomy`（21 处）；
WDA 子进程待登录后看护拉起。
