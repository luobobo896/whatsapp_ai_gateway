#!/bin/bash
# 在 Mac 上把已签名的 WDA Runner 打成 IPA。
# 日常 Windows / Mac 网关只 install 这份包并拉起 XCTest，不再打开 Xcode 工程。
#
# 用法：
#   sh scripts/package-wda-ipa.sh
# 可选环境变量：
#   PROJECT   WhatsAppDeviceAgent 目录（默认仓库旁 ../whatsapp_ai_ios/WhatsAppDeviceAgent）
#   DERIVED   xcodebuild derivedDataPath（默认优先已有产物目录）
#   OUT       输出 IPA（默认 dist/wda.ipa）
#   TEAM      DEVELOPMENT_TEAM（空=工程内写死值）
#   SKIP_BUILD=1  只打包已有 Runner.app，不跑 xcodebuild
#
# 个人开发者账号：目标 iPhone 的 UDID 必须先登记到该账号，再编译/签名，再打这份包。
# 不要把 IPA 提交进 git。

set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

OUT="${OUT:-$ROOT/dist/wda.ipa}"
PROJECT="${PROJECT:-$ROOT/../whatsapp_ai_ios/WhatsAppDeviceAgent}"
APP_NAME="WebDriverAgentRunner-Runner.app"

find_runner_app() {
  local base="$1"
  local cand="$base/Build/Products/Debug-iphoneos/$APP_NAME"
  if [ -d "$cand" ]; then
    printf '%s\n' "$cand"
    return 0
  fi
  return 1
}

APP=""
if [ -n "${DERIVED:-}" ]; then
  APP="$(find_runner_app "$DERIVED" || true)"
fi
if [ -z "$APP" ]; then
  for d in \
    "$HOME/Library/Application Support/WDAFarmGateway/derived" \
    "$ROOT/derived" \
    /tmp/WebDriverAgentFarmDerived
  do
    if APP="$(find_runner_app "$d")"; then
      DERIVED="$d"
      break
    fi
    APP=""
  done
fi

if [ -z "$APP" ] && [ "${SKIP_BUILD:-0}" = "1" ]; then
  echo "找不到 $APP_NAME，且 SKIP_BUILD=1。请设置 DERIVED 指向已有 derived 目录。" >&2
  exit 1
fi

if [ -z "$APP" ]; then
  if [ ! -d "$PROJECT/WebDriverAgent.xcodeproj" ]; then
    echo "找不到 WDA 工程：$PROJECT/WebDriverAgent.xcodeproj" >&2
    echo "请设置 PROJECT，或先在 Xcode 编出 $APP_NAME 再跑本脚本。" >&2
    exit 1
  fi
  DERIVED="${DERIVED:-$ROOT/derived}"
  echo "▶ 未找到已有 Runner.app，运行 build-for-testing → $DERIVED"
  XB=(
    xcodebuild
    -project "$PROJECT/WebDriverAgent.xcodeproj"
    -scheme WebDriverAgentRunner
    -configuration Debug
    -destination "generic/platform=iOS"
    -derivedDataPath "$DERIVED"
    -allowProvisioningUpdates
    ENABLE_DEFAULT_HEADER_SEARCH_PATHS=NO
    GCC_TREAT_WARNINGS_AS_ERRORS=NO
    "OTHER_CFLAGS=\$(inherited) -Wno-error=poison-system-directories"
    RUN_CLANG_STATIC_ANALYZER=NO
  )
  if [ -n "${TEAM:-}" ]; then
    XB+=("DEVELOPMENT_TEAM=$TEAM")
  fi
  XB+=(build-for-testing)
  if ! "${XB[@]}"; then
    echo "build-for-testing 失败（常见是本机缺对应 iOS Platform，exit 70）。" >&2
    echo "请改用已经编过、签过名的 derived（设置 DERIVED），不要在缺 SDK 时反复编。" >&2
    exit 1
  fi
  APP="$(find_runner_app "$DERIVED" || true)"
fi

if [ -z "$APP" ] || [ ! -d "$APP" ]; then
  echo "打包失败：没有 $APP_NAME" >&2
  exit 1
fi

STAGE="$(mktemp -d "${TMPDIR:-/tmp}/wda-ipa.XXXXXX")"
cleanup() { rm -rf "$STAGE"; }
trap cleanup EXIT

mkdir -p "$STAGE/Payload"
ditto "$APP" "$STAGE/Payload/$APP_NAME"
rm -rf "$STAGE/Payload/$APP_NAME/PlugIns/"*.dSYM
mkdir -p "$(dirname "$OUT")"
rm -f "$OUT"
(cd "$STAGE" && zip -qry "$OUT" Payload)

echo "✅ $OUT"
echo "   来自 $APP"
echo "   拷到 Windows/Mac 网关状态目录，命名为 wda.ipa（或启动时 -ipa 指定）"
echo "   网关激活时：未装 Runner 会 install 这份包，再 tidevice xctest / ios runwda"
