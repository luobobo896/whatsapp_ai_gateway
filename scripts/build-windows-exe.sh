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

ARCH="${GOARCH:-amd64}"
case "$ARCH" in
  amd64|386|arm64) ;;
  *)
    echo "不支持的 GOARCH=$ARCH（只用 amd64 / 386 / arm64）" >&2
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

echo "▶ [2/4] 交叉编译 GOOS=windows GOARCH=$ARCH（gateway + probe）"
mkdir -p "$OUT/static"
CGO_ENABLED=0 GOOS=windows GOARCH="$ARCH" go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$EXE" ./cmd/gateway
CGO_ENABLED=0 GOOS=windows GOARCH="$ARCH" go build -trimpath -ldflags "-s -w" -o "$OUT/wda-probe.exe" ./cmd/wda-probe
cp "$ROOT/static/index.html" "$OUT/static/index.html"

echo "▶ [3/4] 桌面壳 WDAFarmGateway.exe（WebView2 + 托盘）"
if [ "${SKIP_DESKTOP:-0}" != "1" ]; then
  CGO_ENABLED=0 GOOS=windows GOARCH="$ARCH" go build -trimpath \
    -ldflags "-s -w -H windowsgui -X main.version=$VERSION" \
    -o "$DESKTOP_EXE" ./cmd/wda-desktop
  echo "  ✓ $DESKTOP_EXE"
else
  echo "  跳过桌面壳（SKIP_DESKTOP=1）"
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
  4. iOS 17+ 需要 wintun.dll（go-ios 隧道）
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
