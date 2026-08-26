#!/bin/bash
# 打包 WDA Farm Gateway 为 macOS .app + DMG（仅 arm64）。
#
# 用法：sh scripts/build-dmg.sh
# 可选环境变量：
#   WDA_PROJECT_DIR  WhatsAppDeviceAgent 工程路径（默认 ../whatsapp_ai_ios/WhatsAppDeviceAgent）
#   SIGN_IDENTITY    指定签名身份；缺省自动检测 "Developer ID Application"，无则 ad-hoc
#   NOTARY_PROFILE   notarytool 钥匙串档案名；设置后对 DMG 公证 + staple
#   SKIP_TESTS=1     跳过 go test
#
# 敏感数据：组装用白名单，仓库根的 devices.json / data/ / .git 永不入包。

set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# 读 gitignored signing.env（WDA_PUBLISHER_TOKEN 等），不存在则跳过。
SIGNING_ENV="${SIGNING_ENV:-$ROOT/scripts/signing.env}"
if [ -f "$SIGNING_ENV" ]; then
  set -a
  # shellcheck disable=SC1090
  . "$SIGNING_ENV"
  set +a
fi

APP_NAME="WDAFarmGateway"
APPDisplayName="WDA Farm Gateway"
VERSION="$(git describe --tags --always 2>/dev/null | sed 's/^v//' || echo dev)"
DMG_NAME="WDAFarmGateway-${VERSION}-arm64.dmg"
BUILD="$ROOT/build"
APP="$BUILD/$APP_NAME.app"
RES="$APP/Contents/Resources"

WDA_PROJECT_DIR="${WDA_PROJECT_DIR:-$ROOT/third_party/WhatsAppDeviceAgent}"

echo "▶ [1/7] Go 测试与构建"
if [ "${SKIP_TESTS:-0}" != "1" ]; then
  go test ./... >/dev/null && echo "  ✓ go test 全绿"
fi
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$BUILD/gateway" ./cmd/gateway

echo "▶ [2/7] Swift 壳构建"
( cd desktop && swift build -c release )
cp "desktop/.build/release/$APP_NAME" "$BUILD/$APP_NAME"

echo "▶ [3/7] 组装 .app（白名单，敏感数据不入包）"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$RES"
cp "$BUILD/$APP_NAME" "$APP/Contents/MacOS/$APP_NAME"
cp "$BUILD/gateway" "$APP/Contents/MacOS/gateway"
cp desktop/Info.plist "$APP/Contents/Info.plist"
cp desktop/AppIcon.icns "$RES/AppIcon.icns"
plutil -replace CFBundleShortVersionString -string "$VERSION" "$APP/Contents/Info.plist"
plutil -replace CFBundleVersion -string "$VERSION" "$APP/Contents/Info.plist"

# Web 管理页
mkdir -p "$RES/static"
cp "$ROOT/static/index.html" "$RES/static/index.html"

# WDA 源码工程（xcodebuild 编译安装到 iPhone 所需；剔除 .git）
if [ ! -d "$WDA_PROJECT_DIR" ]; then
  echo "  ✗ WDA 工程不存在：$WDA_PROJECT_DIR（用 WDA_PROJECT_DIR 覆盖）" >&2
  exit 1
fi
rsync -a --delete \
  --exclude '.git' --exclude 'DerivedData' \
  "$WDA_PROJECT_DIR/" "$RES/WhatsAppDeviceAgent/"

# easytier 二进制 + 授权脚本（安装源；启用时安装到 /usr/local/libexec/wda-gateway）
mkdir -p "$RES/tools/easytier" "$RES/scripts"
cp "$ROOT/tools/easytier/easytier-core" "$ROOT/tools/easytier/easytier-cli" "$RES/tools/easytier/"
cp "$ROOT/scripts/setup-easytier-sudo.sh" "$RES/scripts/"
cp "$ROOT/scripts/install.sh" "$RES/scripts/" 2>/dev/null || true

echo "▶ [4/7] iproxy 随包（USB 隧道必需，含依赖 dylib rpath 修正）"
mkdir -p "$RES/bin" "$RES/lib"
collect_libs() { # $1=已处理文件，输出其 Homebrew 依赖绝对路径（otool 首行是文件自身，跳过）
  otool -L "$1" 2>/dev/null | tail -n +2 | awk '{print $1}' | grep '^/opt/homebrew\|^/usr/local' || true
}
if command -v iproxy >/dev/null 2>&1; then
  IPROXY="$(command -v iproxy)"
  # 拷贝原样二进制与依赖闭包 dylib，不做任何 install_name_tool 修改——
  # 实测修改后的 Mach-O 在 macOS 15 dyld4 下启动死循环挂起（签名 hash 失效，
  # 且 remove-signature+重签也无法挽救）。客户机上由壳/gateway 注入
  # DYLD_FALLBACK_LIBRARY_PATH 指向 Resources/lib 解析 Homebrew 绝对路径引用
  # （dyld 文档化回退：LC_LOAD_DYLIB 的绝对路径不存在时搜索该目录）。
  QUEUE=("$IPROXY")
  IDEV="$(dirname "$IPROXY")/ideviceinfo"
  IDEVID="$(dirname "$IPROXY")/idevice_id"
  IDEVPROV="$(dirname "$IPROXY")/ideviceprovision"
  [ -x "$IDEV" ] && QUEUE+=("$IDEV")
  [ -x "$IDEVID" ] && QUEUE+=("$IDEVID")
  [ -x "$IDEVPROV" ] && QUEUE+=("$IDEVPROV")
  while [ ${#QUEUE[@]} -gt 0 ]; do
    CUR="${QUEUE[0]}"; QUEUE=("${QUEUE[@]:1}")
    for DEP in $(collect_libs "$CUR"); do
      B="$(basename "$DEP")"
      DST="$RES/lib/$B"
      if [ ! -f "$DST" ]; then
        cp -L "$DEP" "$DST"
        QUEUE+=("$DEP")
        SHORT="$(echo "$B" | sed -E 's/-([0-9]+)\.[0-9]+\.dylib/-\1.dylib/')"
        if [ "$SHORT" != "$B" ] && [ ! -e "$RES/lib/$SHORT" ]; then
          ln -s "$B" "$RES/lib/$SHORT"
        fi
      fi
    done
  done
  cp "$IPROXY" "$RES/bin/iproxy"
  [ -x "$IDEV" ] && cp "$IDEV" "$RES/bin/ideviceinfo"
  [ -x "$IDEVID" ] && cp "$IDEVID" "$RES/bin/idevice_id"
  [ -x "$IDEVPROV" ] && cp "$IDEVPROV" "$RES/bin/ideviceprovision"
  echo "  ✓ iproxy/ideviceinfo/idevice_id（原样未修改）+ $(find "$RES/lib" -type f | wc -l | tr -d ' ') 个 dylib（DYLD fallback 解析）"
  [ -x "$RES/bin/ideviceprovision" ] && echo "  ✓ ideviceprovision（自动装描述文件）"
else
  echo "  ⚠ 本机无 iproxy（未装 libimobiledevice），USB 隧道需客户自行 brew install libimobiledevice"
fi

# go-ios / wifi 辅助工具不依赖 iproxy，永远入包（激活 WDA 必需）。
if [ -x "$ROOT/tools/ios" ]; then
  cp "$ROOT/tools/ios" "$RES/bin/ios"
  echo "  ✓ go-ios（ios runwda）"
fi
if [ -x "$ROOT/tools/wifi-lockdown" ]; then
  cp "$ROOT/tools/wifi-lockdown" "$RES/bin/wifi-lockdown"
fi
if [ -x "$ROOT/tools/wifi-runwda" ]; then
  cp "$ROOT/tools/wifi-runwda" "$RES/bin/wifi-runwda"
  echo "  ✓ wifi-runwda（usbmux Network）"
fi
if [ -f "$ROOT/tools/wda.ipa" ]; then
  cp "$ROOT/tools/wda.ipa" "$RES/wda.ipa"
elif [ -f "$ROOT/dist/wda.ipa" ]; then
  cp "$ROOT/dist/wda.ipa" "$RES/wda.ipa"
fi
chmod +w "$RES/bin/"* "$RES/lib/"*.dylib 2>/dev/null || true
[ -f "$RES/wda.ipa" ] && echo "  ✓ wda.ipa"

echo "▶ [5/7] 签名"
SIGN_IDENTITY="${SIGN_IDENTITY:-}"
if [ -z "$SIGN_IDENTITY" ]; then
  SIGN_IDENTITY="$(security find-identity -v -p codesigning 2>/dev/null | grep "Developer ID Application" | head -1 | sed -E 's/.*"(.*)"/\1/' || true)"
fi
if [ -n "$SIGN_IDENTITY" ]; then
  ENT=""
  if [ -f "$ROOT/scripts/notarization-entitlements.plist" ]; then ENT="--entitlements $ROOT/scripts/notarization-entitlements.plist"; fi
  # 公证要求包内每个可执行/dylib 都用同一身份签名；codesign --deep 不会签 Resources 里散落的裸 Mach-O，
  # 对 bin/tools/lib 下的每个 Mach-O 逐个体补签（含 hardened runtime + 时间戳 + entitlement）。
  find "$RES" -type f 2>/dev/null | while read -r f; do
    file "$f" 2>/dev/null | grep -q "Mach-O" || continue
    codesign --force --options runtime --timestamp $ENT --sign "$SIGN_IDENTITY" "$f" >/dev/null 2>&1 \
      || echo "  ⚠ 补签失败: $f"
  done
  codesign --deep --force --options runtime --timestamp $ENT --sign "$SIGN_IDENTITY" "$APP"
  echo "  ✓ Developer ID 签名：$SIGN_IDENTITY"
  SIGNED="developer-id"
else
  codesign --deep --force --sign - "$APP"
  echo "  ⚠ 未找到 Developer ID 证书，已 ad-hoc 签名（客户首次打开需右键→打开）"
  SIGNED="ad-hoc"
fi

echo "▶ [6/7] 安装说明"
if [ "$SIGNED" = "developer-id" ] && [ -n "${NOTARY_PROFILE:-}" ]; then
  OPEN_HINT="双击打开 WDAFarmGateway.app 即可（已签名公证）。"
else
  OPEN_HINT="首次打开：右键点 WDAFarmGateway.app →「打开」→ 再点「打开」（或系统设置→隐私与安全性→仍要打开）。"
fi
cat > "$BUILD/安装说明.txt" <<EOF
WDA Farm Gateway（${VERSION}）安装说明
====================================

一、先装依赖（仅一次）
  1. App Store 安装 Xcode（装完打开一次，按提示装完组件）；
  2. iPhone 用数据线连到 Mac，手机上点「信任」；
  3. 在 iPhone「设置 → 隐私与安全性」打开「开发者模式」（iOS 16+）。

二、安装本应用
  1. 把 WDAFarmGateway.app 拖入「应用程序」文件夹；
  2. ${OPEN_HINT}

三、首次启动会出现的系统弹窗（全部点允许）
  - 「允许接收传入网络连接」（本机 8300 端口服务，局域网设备可访问管理页）；
  - 「本地网络」访问权限（发现局域网内的 iPhone）。

四、登录与设备
  - 管理页账号与云平台一致（邮箱/密码）；
  - 插上 iPhone 后在设备列表点「激活」安装 WDA；
  - 激活会自动安装描述文件并尝试点「信任」（个人/企业同一套）。若手机弹出锁屏密码请输入；
  - 激活 WDA 首次会编译（数分钟），之后秒级。

五、机型/系统兼容
  - 支持 iPhone 7 及以上机型、iOS 15 及以上系统；
  - iOS 16+ 需先在 iPhone「设置 → 隐私与安全性」打开「开发者模式」；
  - iOS 15/16 老设备（如 iPhone 7/8/X）激活时，网关会自动从本机 Xcode 复制
    匹配的 DeveloperDiskImage 到设备支持目录（无需手工干预）；若本机 Xcode
    未包含 iOS 15 镜像，请升级 Xcode 或手动放置对应版本 DeveloperDiskImage.dmg；
  - 老机型（40 位 UDID，iOS ≤15）与新款（连字符 UDID，iOS 16+）已分别按
    xcodebuild 兼容格式自动匹配，无需关心大小写/前缀。

五、可选：easytier 组网后备通道
  默认关闭。启用时按页面提示完成一次性授权（安装到 /usr/local/libexec/wda-gateway 并放行 sudoers）。

六、排障
  - 菜单栏「◉ 网关」→ 打开日志：~/Library/Application Support/WDAFarmGateway/logs/gateway.log
  - 数据目录：~/Library/Application Support/WDAFarmGateway/
  - 局域网访问地址：菜单栏「复制局域网访问地址」（http://<本机IP>:8300）
EOF

echo "▶ [7/7] DMG"
STAGING="$BUILD/dmg-staging"
rm -rf "$STAGING" "$ROOT/$DMG_NAME"
mkdir -p "$STAGING"
cp -R "$APP" "$STAGING/"
cp "$BUILD/安装说明.txt" "$STAGING/"
ln -s /Applications "$STAGING/Applications"
hdiutil create -volname "$APPDisplayName" -srcfolder "$STAGING" -ov -format UDZO "$ROOT/$DMG_NAME" >/dev/null
echo "  ✓ $ROOT/$DMG_NAME ($(du -h "$ROOT/$DMG_NAME" | cut -f1))"

# 敏感数据终检：挂载 DMG，确认包内不含真实 token / api_key / 本地数据目录
echo "▶ 敏感数据终检"
hdiutil attach -nobrowse -readonly "$ROOT/$DMG_NAME" >/dev/null
MNT="/Volumes/$APPDisplayName"
FOUND=0
# 检查包内不含仓库 devices.json 的真实凭证（云 token / LLM key）
REAL_TOKEN=$(python3 -c "import json;print(json.load(open('$ROOT/devices.json'))['cloud']['token'])" 2>/dev/null || true)
REAL_KEY=$(python3 -c "import json;print(json.load(open('$ROOT/devices.json'))['llm']['api_key'][:24])" 2>/dev/null || true)
if [ -n "$REAL_TOKEN" ] && grep -rq "$REAL_TOKEN" "$MNT" 2>/dev/null; then echo "  ✗ 泄漏：真实云 token 出现在包内"; FOUND=1; fi
if [ -n "$REAL_KEY" ] && grep -rq "$REAL_KEY" "$MNT" 2>/dev/null; then echo "  ✗ 泄漏：真实 LLM api_key 出现在包内"; FOUND=1; fi
if [ -e "$MNT/$APP_NAME.app/Contents/Resources/devices.json" ]; then echo "  ✗ 泄漏：devices.json 入包"; FOUND=1; fi
if [ -e "$MNT/$APP_NAME.app/Contents/Resources/data" ]; then echo "  ✗ 泄漏：data/ 入包"; FOUND=1; fi
hdiutil detach "$MNT" >/dev/null 2>&1 || true
[ "$FOUND" = "0" ] && echo "  ✓ 无敏感数据泄漏"

if [ "$SIGNED" = "developer-id" ] && [ -n "${NOTARY_PROFILE:-}" ]; then
  echo "▶ 公证 + staple"
  xcrun notarytool submit "$ROOT/$DMG_NAME" --keychain-profile "$NOTARY_PROFILE" --wait
  xcrun stapler staple "$ROOT/$DMG_NAME"
  echo "  ✓ 已公证"
else
  echo "ℹ 跳过公证（未设置 NOTARY_PROFILE）。有 Developer ID 后："
  echo "   xcrun notarytool store-credentials <profile> --apple-id <账号> --team-id <TeamID>"
  echo "   NOTARY_PROFILE=<profile> sh scripts/build-dmg.sh"
fi

# 只保留最新 DMG（手册补充：避免仓库根堆积）
for f in "$ROOT"/WDAFarmGateway-*.dmg; do
  [ -e "$f" ] || continue
  [ "$f" = "$ROOT/$DMG_NAME" ] || { echo "  › 清理旧 dmg: $f"; rm -f "$f"; }
done
# 记录最新产物信息到 dist/latest.json（忽略入 git）
mkdir -p "$ROOT/dist"
SHA="$(shasum -a 256 "$ROOT/$DMG_NAME" | awk '{print $1}')"
SIZE="$(stat -f%z "$ROOT/$DMG_NAME")"
python3 - "$ROOT/dist/latest.json" <<PY
import json, sys
json.dump({"path": "$ROOT/$DMG_NAME", "version": "$VERSION", "commit": "$(git rev-parse --short HEAD)", "sha256": "$SHA", "size": $SIZE, "built_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"}, open(sys.argv[1], "w"), indent=2, ensure_ascii=False)
print("  › 记录最新产物: dist/latest.json")
PY

echo "✅ 完成：$ROOT/$DMG_NAME"

# 自动上传到云平台（同平台覆盖，只留最新）；未配置 WDA_PUBLISHER_TOKEN 则跳过。
if [ -n "${WDA_PUBLISHER_TOKEN:-}" ]; then
  sh scripts/upload-release.sh "$ROOT/$DMG_NAME" darwin arm64 "$VERSION" || true
fi
