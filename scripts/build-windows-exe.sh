#!/bin/bash
# 交叉编译 Windows 网关 + 桌面壳（不含 Xcode，激活走 go-ios/tidevice）。
#
# 用法：
#   sh scripts/build-windows-exe.sh
# 可选：
#   GOARCH=amd64|386|arm64   默认 amd64
#   SKIP_TESTS=1             跳过 go test
#   SKIP_DESKTOP=1           不编 WDAFarmGateway.exe 桌面壳
#
# 产出：dist/windows-<arch>/gateway.exe + wda-probe.exe + WDAFarmGateway.exe + 使用说明.txt
# 不打包 devices.json / data / 密钥。

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

ARCH="${GOARCH:-amd64}"
case "$ARCH" in
  amd64|386|arm64) ;;
  *)
    echo "不支持的 GOARCH=${ARCH}（只用 amd64 / 386 / arm64）" >&2
    exit 1
    ;;
esac

VERSION="$(git describe --tags --always 2>/dev/null | sed "s/^v//" || echo dev)"
OUT="$ROOT/dist/windows-$ARCH"
EXE="$OUT/gateway.exe"
DESKTOP_EXE="$OUT/WDAFarmGateway.exe"

echo "▶ [1/4] 测试"
if [ "${SKIP_TESTS:-0}" != "1" ]; then
  go test ./...
  echo "  ✓ go test 通过"
else
  echo "  跳过测试（SKIP_TESTS=1）"
fi

echo "▶ [2/4] 交叉编译 GOOS=windows GOARCH=${ARCH}（gateway + probe）"
mkdir -p "$OUT/static"
CGO_ENABLED=0 GOOS=windows GOARCH="$ARCH" go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$EXE" ./cmd/gateway
CGO_ENABLED=0 GOOS=windows GOARCH="$ARCH" go build -trimpath -ldflags "-s -w" -o "$OUT/wda-probe.exe" ./cmd/wda-probe
cp "$ROOT/static/index.html" "$OUT/static/index.html"

echo "▶ [2.5/4] 激活辅助二进制（Mac/Windows 同一套业务逻辑；netmuxd 随仓分发）"
mkdir -p "$OUT/bin"
# 清理已被 netmuxd 取代的旧 usbmux-bridge 残留（历史构建可能还在）。
rm -f "$OUT/bin/usbmux-bridge.exe"
CGO_ENABLED=0 GOOS=windows GOARCH="$ARCH" go build -trimpath -ldflags "-s -w" \
  -o "$OUT/bin/wifi-runwda.exe" ./cmd/wifi-runwda
( cd "$ROOT/cmd/wifi-lockdown" && CGO_ENABLED=0 GOOS=windows GOARCH="$ARCH" go build -trimpath -ldflags "-s -w" \
  -o "$OUT/bin/wifi-lockdown.exe" . )
# netmuxd：Windows 上提供 ConnectionType=Network 条目（mDNS 发现 + heartbeat 保活），
# USB 经 shim 模式转发 AMDS，使无线 WDA 拔掉 USB 后不断开。二进制来自官方 release，
# 随 tools/ 分发（LGPL-2.1，附 netmuxd-LICENSE.txt）。
cp "$ROOT/tools/netmuxd.exe" "$OUT/bin/netmuxd.exe"
[ -f "$ROOT/tools/netmuxd-LICENSE.txt" ] && cp "$ROOT/tools/netmuxd-LICENSE.txt" "$OUT/bin/"
cp "$OUT/bin/wifi-runwda.exe" "$ROOT/tools/wifi-runwda.exe"
cp "$OUT/bin/wifi-lockdown.exe" "$ROOT/tools/wifi-lockdown.exe"
for b in ios.exe idevice_id.exe ideviceinfo.exe iproxy.exe; do
  [ -x "$ROOT/tools/$b" ] && cp "$ROOT/tools/$b" "$OUT/bin/" 2>/dev/null || true
done
if [ -f "$ROOT/tools/wda.ipa" ]; then
  cp "$ROOT/tools/wda.ipa" "$OUT/wda.ipa"
elif [ -f "$ROOT/dist/wda.ipa" ]; then
  cp "$ROOT/dist/wda.ipa" "$OUT/wda.ipa"
fi
ls "$OUT/bin/" 2>/dev/null || true

echo "▶ [3/4] 桌面壳 WDAFarmGateway.exe（WebView2 + 托盘）"
if [ "${SKIP_DESKTOP:-0}" != "1" ]; then
  CGO_ENABLED=0 GOOS=windows GOARCH="$ARCH" go build -trimpath \
    -ldflags "-s -w -H windowsgui -X main.version=$VERSION" \
    -o "$DESKTOP_EXE" ./cmd/wda-desktop
  echo "  ✓ $DESKTOP_EXE"
else
  echo "  跳过桌面壳（SKIP_DESKTOP=1）"
fi

# Windows Authenticode 签名：与 Mac 的 codesign 同业务逻辑（有证书就签，没证书就警告未签名）。
# 依赖 osslsigncode（macOS: brew install osslsigncode）；证书用 .pfx/.p12，凭证放 scripts/signing.env：
#   WIN_SIGN_PFX=/path/to/证书.pfx
#   WIN_SIGN_PASSWORD=证书密码
echo "▶ [3.5/4] Windows Authenticode 签名"
if command -v osslsigncode >/dev/null 2>&1 && [ -n "${WIN_SIGN_PFX:-}" ]; then
  sign_pe() {
    osslsigncode sign -h sha256 -t "http://timestamp.digicert.com" \
      -pkcs12 "$WIN_SIGN_PFX" -pass "$WIN_SIGN_PASSWORD" inplace "$1" >/dev/null 2>&1 \
      && echo "  ✓ sign $1" || echo "  ✗ sign 失败: $1"
  }
  sign_pe "$EXE"
  [ -f "$DESKTOP_EXE" ] && sign_pe "$DESKTOP_EXE"
  for b in "$OUT"/bin/*.exe; do [ -e "$b" ] && sign_pe "$b"; done
else
  echo "  ⚠ 未配置 Windows 签名证书（WIN_SIGN_PFX）或 osslsigncode，产物未签名（SmartScreen 会提示未知发布者）"
fi

# 使用说明（python 写文件，避免嵌套 heredoc）
python3 - "$OUT" "$VERSION" "$ARCH" <<"PY"
import pathlib, sys
out, version, arch = pathlib.Path(sys.argv[1]), sys.argv[2], sys.argv[3]
text = f"""WDA Farm Gateway {version} (Windows {arch})

【推荐】桌面客户端（无需手动开浏览器）：
  双击 WDAFarmGateway.exe
  → 系统托盘常驻，并自动打开管理窗口（WebView2 加载 http://127.0.0.1:8300/）
  需已安装 Microsoft Edge WebView2 Runtime（Win10/11 通常自带）。
  数据目录：%AppData%\\WDAFarmGateway\\
  日志：%AppData%\\WDAFarmGateway\\logs\\

【兼容】仅网关（自行开浏览器）：
  gateway.exe
  浏览器打开 http://127.0.0.1:8300/

激活后端：Windows 默认走 go-ios（ios.exe runwda），不调用 xcodebuild。
把 ios.exe（或 tidevice.exe）以及 Windows 版 idevice_id / ideviceinfo / iproxy
放到 PATH，或放到本目录 bin\\（壳会把 WDA_GATEWAY_RESOURCES 指到本目录）。

把 Mac 上 scripts/package-wda-ipa.sh 打好的 wda.ipa 放到本目录（或 -state / %AppData%\\WDAFarmGateway）。
点激活时若手机还没装 Runner，网关会先 install 再拉起。

本机还需要：
  1. Apple Devices 或 iTunes（提供 usbmux）
  2. iPhone 已配对、已开开发者模式、已信任开发者证书
  3. wda.ipa（或手机上已装着同一签名的 WebDriverAgentRunner，bundle 默认 com.wda.WebRunner.xctrunner）
  4. iOS 17.4+ 由网关自动 ios tunnel start --userspace（一般不用 wintun）。只有 17.0–17.3 内核隧道才需要 wintun.dll
  5. 桌面壳：WebView2 Runtime

连发探针（WDA 已在 127.0.0.1:18100 ready 时）：
  wda-probe.exe -wda http://127.0.0.1:18100 -phone 15213472085 -text "hello nice to see you" -send -count 3 -interval 1

当晚步骤见仓库 docs/deployment/windows-night-runbook.md。
桌面壳说明见 docs/deployment/windows-desktop-shell.md。

easytier（可选，默认关）：平时发消息不用管理员。
只有平台要开虚拟网卡时，才右键「以管理员身份运行」。
Mac 上那套 sudo sh scripts/install.sh 是苹果电脑专用，Windows 不要跑。

不要把 gateway.db、devices.json、云凭证打进分发包。
"""
(out / "使用说明.txt").write_text(text, encoding="utf-8")
PY

echo "▶ [4/4] 校验"
if command -v file >/dev/null 2>&1; then
  file "$EXE" || true
  [ -f "$DESKTOP_EXE" ] && file "$DESKTOP_EXE" || true
fi
ls -lh "$EXE" "$OUT/wda-probe.exe" 2>/dev/null || true
[ -f "$DESKTOP_EXE" ] && ls -lh "$DESKTOP_EXE"
echo "✅ $OUT"

# 打包成 zip 并自动上传云平台（同平台覆盖，只留最新）；未配置 WDA_PUBLISHER_TOKEN 则跳过。
if [ -n "${WDA_PUBLISHER_TOKEN:-}" ]; then
  ZIP="${TMPDIR:-/tmp}/wda-gateway-windows-${ARCH}.zip"
  (cd "$OUT" && zip -qr "$ZIP" .)
  sh scripts/upload-release.sh "$ZIP" windows "$ARCH" "$VERSION" || true
fi
