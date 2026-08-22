#!/bin/bash
# 交叉编译 Windows 网关可执行文件（不含 Xcode，激活走 go-ios/tidevice）。
#
# 用法：
#   sh scripts/build-windows-exe.sh
# 可选：
#   GOARCH=amd64|386|arm64   默认 amd64
#   SKIP_TESTS=1             跳过 go test
#
# 产出：dist/windows-<arch>/gateway.exe + 使用说明.txt
# 不打包 devices.json / data / 密钥。Windows 上的 Apple Devices、ios.exe、
# iproxy 需另行安装，见产出目录里的说明。

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

VERSION="$(git describe --tags --always 2>/dev/null | sed 's/^v//' || echo dev)"
OUT="$ROOT/dist/windows-$ARCH"
EXE="$OUT/gateway.exe"

echo "▶ [1/3] 测试"
if [ "${SKIP_TESTS:-0}" != "1" ]; then
  go test ./...
  echo "  ✓ go test 通过"
else
  echo "  跳过测试（SKIP_TESTS=1）"
fi

echo "▶ [2/3] 交叉编译 GOOS=windows GOARCH=$ARCH"
mkdir -p "$OUT/static"
CGO_ENABLED=0 GOOS=windows GOARCH="$ARCH" go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$EXE" ./cmd/gateway
cp "$ROOT/static/index.html" "$OUT/static/index.html"

cat > "$OUT/使用说明.txt" <<EOF
WDA Farm Gateway ${VERSION} (Windows ${ARCH})

启动（在本目录）：
  gateway.exe
  浏览器打开 http://127.0.0.1:8300/

激活后端：Windows 默认走 go-ios（ios.exe runwda），不调用 xcodebuild。
把 ios.exe（或 tidevice.exe）以及 Windows 版 idevice_id / ideviceinfo / iproxy
放到 PATH，或放到 WDA_GATEWAY_RESOURCES\\bin。

本机还需要：
  1. Apple Devices 或 iTunes（提供 usbmux）
  2. iPhone 已配对、已开开发者模式、已信任开发者证书
  3. 手机上已安装签过名的 WebDriverAgentRunner（bundle 默认 com.wda.WebRunner.xctrunner）
  4. iOS 17+ 需要 wintun.dll（go-ios 隧道）

不要把 gateway.db、devices.json、云凭证打进分发包。
EOF

echo "▶ [3/3] 校验"
if command -v file >/dev/null 2>&1; then
  file "$EXE"
fi
ls -lh "$EXE"
echo "✅ $EXE"
