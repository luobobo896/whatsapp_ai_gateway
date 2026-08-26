#!/bin/bash
# 在 Mac 上把已签名的 WDA Runner 打成 IPA。
# 日常 Windows / Mac 网关只 install 这份包并拉起 XCTest，不再打开 Xcode 工程。
#
# 用法：
#   sh scripts/package-wda-ipa.sh
# 可选：把 scripts/signing.env.example 拷成 scripts/signing.env 后改 SIGN_MODE。
#
# 环境变量：
#   PROJECT   WhatsAppDeviceAgent 目录（默认仓库旁 ../whatsapp_ai_ios/WhatsAppDeviceAgent）
#   DERIVED   xcodebuild derivedDataPath（默认优先已有产物目录）
#   OUT       输出 IPA（默认 dist/wda.ipa）
#   TEAM      DEVELOPMENT_TEAM（空=工程内写死值；企业可从描述文件补）
#   SIGN_MODE auto|personal|enterprise（默认 auto：有描述文件跟文件走，否则个人）
#   IDENTITY  CODE_SIGN_IDENTITY（企业必填，如 iPhone Distribution: 公司名）
#   PROFILE   In-House .mobileprovision 路径或 UUID
#   PROFILE_SPECIFIER  Xcode 里的描述文件名
#   SIGNING_ENV  额外 env 文件（默认 scripts/signing.env，存在才读）
#   SKIP_BUILD=1  只打包已有 Runner.app，不跑 xcodebuild
#   FORCE_BUILD=1 忽略已有 Runner.app，按当前 SIGN_MODE 重编
#
# 个人付费账号：目标 iPhone 的 UDID 必须先登记，描述文件会列出设备。
# 企业 In-House：描述文件必须 ProvisionsAllDevices，不绑 UDID。
# 不要把 IPA、证书、描述文件提交进 git。

set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

SIGNING_ENV="${SIGNING_ENV:-$ROOT/scripts/signing.env}"
if [ -f "$SIGNING_ENV" ]; then
  # shellcheck disable=SC1090
  set -a
  . "$SIGNING_ENV"
  set +a
fi

OUT="${OUT:-$ROOT/dist/wda.ipa}"
PROJECT="${PROJECT:-$ROOT/third_party/WhatsAppDeviceAgent}"
APP_NAME="WebDriverAgentRunner-Runner.app"
SIGN_MODE="${SIGN_MODE:-auto}"
IDENTITY="${IDENTITY:-}"
PROFILE="${PROFILE:-}"
PROFILE_SPECIFIER="${PROFILE_SPECIFIER:-}"
TEAM="${TEAM:-}"

inspect() {
  if ! command -v go >/dev/null 2>&1; then
    echo "打包需要本机 Go，用来检查描述文件是个人还是企业。" >&2
    exit 1
  fi
  (cd "$ROOT" && go run ./cmd/ipa-inspect "$@")
}

install_profile_file() {
  local src="$1"
  if [ ! -f "$src" ]; then
    echo "找不到描述文件：$src" >&2
    exit 1
  fi
  local uuid
  uuid="$(inspect -profile "$src" -field uuid)"
  if [ -z "$uuid" ]; then
    echo "描述文件没有 UUID：$src" >&2
    exit 1
  fi
  if [ -z "${TEAM:-}" ]; then
    TEAM="$(inspect -profile "$src" -field team)"
  fi
  local dest_dir="$HOME/Library/MobileDevice/Provisioning Profiles"
  mkdir -p "$dest_dir"
  cp "$src" "$dest_dir/${uuid}.mobileprovision"
  echo "$uuid"
}

resolve_desired_mode() {
  local args=(-resolve -sign-mode "$SIGN_MODE" -identity "$IDENTITY")
  if [ -n "$PROFILE" ] && [ -f "$PROFILE" ]; then
    args+=(-profile "$PROFILE")
  fi
  inspect "${args[@]}"
}

find_runner_app() {
  local base="$1"
  local cand="$base/Build/Products/Debug-iphoneos/$APP_NAME"
  if [ -d "$cand" ]; then
    printf '%s\n' "$cand"
    return 0
  fi
  return 1
}

PROFILE_UUID=""
if [ -n "$PROFILE" ]; then
  case "$PROFILE" in
    *.mobileprovision)
      PROFILE_UUID="$(install_profile_file "$PROFILE")"
      ;;
    *)
      PROFILE_UUID="$PROFILE"
      ;;
  esac
fi

if ! DESIRED_MODE="$(resolve_desired_mode)"; then
  exit 1
fi

if [ "$DESIRED_MODE" = "enterprise" ]; then
  if [ -z "$IDENTITY" ]; then
    echo "企业打包需要 IDENTITY（例如 iPhone Distribution: 公司名）" >&2
    exit 1
  fi
  if [ -z "$PROFILE_SPECIFIER" ] && [ -z "$PROFILE_UUID" ]; then
    echo "企业打包需要 PROFILE_SPECIFIER 或 PROFILE（In-House .mobileprovision）" >&2
    exit 1
  fi
  if [ -n "$PROFILE" ] && [ -f "$PROFILE" ]; then
    inspect -profile "$PROFILE" -require enterprise >/dev/null
  fi
fi

echo "▶ SIGN_MODE=$SIGN_MODE → $DESIRED_MODE"

APP=""
if [ "${FORCE_BUILD:-0}" != "1" ] && [ -n "${DERIVED:-}" ]; then
  APP="$(find_runner_app "$DERIVED" || true)"
fi
if [ "${FORCE_BUILD:-0}" != "1" ] && [ -z "$APP" ]; then
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

if [ -n "$APP" ] && [ -f "$APP/embedded.mobileprovision" ]; then
  EXISTING_MODE="$(inspect -app "$APP" -field mode)"
  if [ "$EXISTING_MODE" != "$DESIRED_MODE" ]; then
    if [ "${SKIP_BUILD:-0}" = "1" ]; then
      echo "已有 Runner.app 是 $EXISTING_MODE 签名，与 $DESIRED_MODE 不符，且 SKIP_BUILD=1。" >&2
      echo "请去掉 SKIP_BUILD，或换一份匹配的 DERIVED。" >&2
      exit 1
    fi
    echo "▶ 已有 Runner.app 是 $EXISTING_MODE，目标是 $DESIRED_MODE，将重编"
    APP=""
  elif [ "$DESIRED_MODE" = "enterprise" ]; then
    inspect -app "$APP" -require enterprise >/dev/null
  fi
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
  echo "▶ 未找到匹配的 Runner.app，运行 build-for-testing → $DERIVED"
  XB=(
    xcodebuild
    -project "$PROJECT/WebDriverAgent.xcodeproj"
    -scheme WebDriverAgentRunner
    -configuration Debug
    -destination "generic/platform=iOS"
    -derivedDataPath "$DERIVED"
    ENABLE_DEFAULT_HEADER_SEARCH_PATHS=NO
    GCC_TREAT_WARNINGS_AS_ERRORS=NO
    "OTHER_CFLAGS=\$(inherited) -Wno-error=poison-system-directories"
    RUN_CLANG_STATIC_ANALYZER=NO
  )
  if [ "$DESIRED_MODE" != "enterprise" ]; then
    XB+=(-allowProvisioningUpdates)
  fi
  if [ -n "${TEAM:-}" ]; then
    XB+=("DEVELOPMENT_TEAM=$TEAM")
  fi
  if [ "$DESIRED_MODE" = "enterprise" ]; then
    XB+=(
      CODE_SIGN_STYLE=Manual
      "CODE_SIGN_IDENTITY=$IDENTITY"
    )
    if [ -n "$PROFILE_SPECIFIER" ]; then
      XB+=("PROVISIONING_PROFILE_SPECIFIER=$PROFILE_SPECIFIER")
    fi
    if [ -n "$PROFILE_UUID" ]; then
      XB+=("PROVISIONING_PROFILE=$PROFILE_UUID")
    fi
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

if [ -f "$APP/embedded.mobileprovision" ]; then
  inspect -app "$APP" -require "$DESIRED_MODE" >/dev/null
fi

STAGE="$(mktemp -d "${TMPDIR:-/tmp}/wda-ipa.XXXXXX")"
cleanup() { rm -rf "$STAGE"; }
trap cleanup EXIT

mkdir -p "$STAGE/Payload"
ditto "$APP" "$STAGE/Payload/$APP_NAME"
# Do NOT delete files inside the signed .app (e.g. PlugIns/*.dSYM) — that triggers
# ApplicationVerificationFailed 0xe8008017 (resource added/modified/deleted).
mkdir -p "$(dirname "$OUT")"
rm -f "$OUT"
(cd "$STAGE" && zip -qry "$OUT" Payload)

echo "✅ $OUT"
echo "   来自 $APP"
if [ -f "$APP/embedded.mobileprovision" ]; then
  INFO="$(inspect -ipa "$OUT")"
  python3 -c 'import json,sys; i=json.loads(sys.argv[1]); print("   签名: {mode}  team={team}  devices={device_count}  all_devices={provisions_all_devices}".format(**i))' "$INFO"
  inspect -ipa "$OUT" -require "$DESIRED_MODE" >/dev/null
fi
echo "   拷到 Windows/Mac 网关状态目录，命名为 wda.ipa（或启动时 -ipa 指定）"
echo "   网关激活时：未装 Runner 会 install 这份包，再 tidevice xctest / ios runwda"
if [ "$DESIRED_MODE" = "enterprise" ]; then
  echo "   企业包不绑 UDID。网关激活时会自动安装描述文件并尝试点「信任」"
else
  echo "   个人包仍须先登记 UDID 再出包。网关激活时会自动安装描述文件并尝试点「信任」"
fi
