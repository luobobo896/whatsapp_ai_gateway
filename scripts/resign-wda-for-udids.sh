#!/bin/bash
# 个人开发者账号：按 UDID 列表自动重签 wda.ipa（企业账号免此流程）。
#
# 用法：
#   sh scripts/resign-wda-for-udids.sh --self-check
#   sh scripts/resign-wda-for-udids.sh --mode personal --udids "595249...,00008120-..."
#   sh scripts/resign-wda-for-udids.sh --mode personal --udids-file <file> [--upload <dst>]
#   sh scripts/resign-wda-for-udids.sh --mode personal --udids-url <云平台返回JSON数组的URL>
#   需要：FASTLANE_API_KEY_PATH / FASTLANE_ISSUER_ID / FASTLANE_KEY_ID（个人）；WDA_API_TOKEN（平台发布者 token）
#   sh scripts/resign-wda-for-udids.sh --mode enterprise ...        # 企业免登记
#
# 可选 env：APPLE_ID / API_KEY 或 FASTLANE_API_KEY / FASTLANE_TEAM_ID / BUNDLE_ID。
# 注意：真正的"自动登记新 UDID 到 Apple 后台"必须 fastlane（或 Apple Developer API），且证书 Team
#       与 ipa profile Team 必须一致。缺少会在此自检/签名阶段直接报错，不会假装成功。

set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# 读 gitignored 的 signing.env（WDA_API_TOKEN / FASTLANE_* 等），不存在则跳过。
SIGNING_ENV="${SIGNING_ENV:-$ROOT/scripts/signing.env}"
if [ -f "$SIGNING_ENV" ]; then
  set -a
  # shellcheck disable=SC1090
  . "$SIGNING_ENV"
  set +a
fi

MODE="personal"
UDIDS_ARG=""
UDIDS_FILE=""
UDIDS_URL=""
UPLOAD_DST=""
BUNDLE_ID="${BUNDLE_ID:-com.wda.WebRunner.xctrunner}"
SELF_CHECK=0

while [ $# -gt 0 ]; do
  case "$1" in
    --mode) MODE="$2"; shift 2 ;;
    --udids) UDIDS_ARG="$2"; shift 2 ;;
    --udids-file) UDIDS_FILE="$2"; shift 2 ;;
    --udids-url) UDIDS_URL="$2"; shift 2 ;;
    --upload) UPLOAD_DST="$2"; shift 2 ;;
    --self-check) SELF_CHECK=1; shift ;;
    -h|--help) grep '^#' "$0"; exit 0 ;;
    *) echo "未知参数：$1" >&2; exit 2 ;;
  esac
done

log(){ echo "▶ $*"; }
die(){ echo "✗ $*" >&2; exit 1; }

have(){ command -v "$1" >/dev/null 2>&1; }

# 读 Profile 关键信息的辅助（复用 cmd/ipa-inspect）
inspect(){ go run ./cmd/ipa-inspect "$@"; }

ipa_team(){
  local t
  t="$(inspect -ipa "${1:-tools/wda.ipa}" -field team 2>/dev/null || true)"
  printf '%s' "$t"
}

check_dev_cert(){
  security find-identity -v -p codesigning 2>/dev/null \
    | grep -q "Apple Development" && { log "✓ 找到 Apple Development 证书"; return 0; }
  die "未找到 Apple Development 证书（个人签名需要）。请确认 Xcode 已登录且证书在钥匙串。"
}

self_check(){
  echo "===== 前置自检（个人开发者自动重签）====="
  echo "MODE=$MODE  BUNDLE_ID=$BUNDLE_ID"
  if have fastlane; then log "✓ fastlane 已安装"; else die "未安装 fastlane（brew install fastlane）。没有它无法把「新 UDID」注册进 Apple 后台并生成 profile；xcodebuild 只对已登记 UDID 更新 profile。" ; fi
  check_dev_cert
  local it ip
  it="$(ipa_team 2>/dev/null || true)"
  ip="$(security find-identity -v -p codesigning 2>/dev/null | grep -o 'Apple Development: [^(]*([A-Z0-9]*)' | head -1 || true)"
  echo "ipa profile TeamIdentifier = ${it:-?}；开发证书账号 = ${ip:-?}"
  [ -n "$it" ] && [ -n "$ip" ] && ! echo "$ip" | grep -q "$it" \
    && die "证书账号与 ipa Team 不一致：证书=${ip}，ipa Team=${it}。需统一签名 Team 后重签。" || true
  echo "===== 自检完成 ====="
}

[ "$SELF_CHECK" = "1" ] && { self_check; exit 0; }

# 收集 UDID（参数 > 文件 > URL）
UDIDS="$UDIDS_ARG"
if [ -z "$UDIDS" ] && [ -n "$UDIDS_FILE" ]; then
  UDIDS="$(tr '\n' ',' < "$UDIDS_FILE" 2>/dev/null || die "读不到 UDIDS_FILE=$UDIDS_FILE")"
fi
if [ -z "$UDIDS" ] && [ -n "$UDIDS_URL" ]; then
  have curl || die "需要 curl 拉取云平台 UDID 表"
  [ -n "${WDA_API_TOKEN:-}" ] || die "拉取云平台 UDID 需要 WDA_API_TOKEN（平台发布者 token）"
  UDIDS="$(curl -fsSL --max-time 20 -H "Authorization: Bearer $WDA_API_TOKEN" "$UDIDS_URL" 2>/dev/null | sed -e 's/[[\]"," ]//g' | tr '\n' ',' || die "拉取云平台 UDID 失败")"
fi
UDIDS="$(echo "$UDIDS" | tr ',;' '\n' | sed 's/^[[:space:]]*//;s/[[:space:]]*$//' | grep -v '^$' | tr '\n' ',' | sed 's/,$//')"

case "$MODE" in
  personal)
    [ -n "$UDIDS" ] || die "personal 模式需要 UDID 列表（--udids / --udids-file / --udids-url）以生成对应 profile"
    have fastlane || die "缺少 fastlane：无法把 UDID（$UDIDS）自动注册进 Apple 后台。请 brew install fastlane 并提供 APPLE_ID/API_KEY 后再试。"
    check_dev_cert
    echo "将针对以下 UDID 重签：$UDIDS"
    ;;
  enterprise)
    echo "企业模式：免登记。请确保已提供 IDENTITY / PROFILE_SPECIFIER / PROFILE（见 package-wda-ipa.sh）"
    ;;
  *) die "MODE 只能是 personal|enterprise" ;;
esac

# 出包：个人先注册新机到团队再重签；企业直接出企业包（--mode 决定，均可配置）。
if [ "$MODE" = "enterprise" ]; then
  echo "▶ 企业模式：直接出企业包（免登记，需 IDENTITY/PROFILE，见 package-wda-ipa.sh）"
  SIGN_MODE=enterprise sh scripts/package-wda-ipa.sh
else
  have fastlane || die "需要 fastlane（brew install fastlane）"
  [ -n "${FASTLANE_API_KEY_PATH:-}" ] && [ -n "${FASTLANE_ISSUER_ID:-}" ] && [ -n "${FASTLANE_KEY_ID:-}" ] \
    || die "需要 FASTLANE_API_KEY_PATH / FASTLANE_ISSUER_ID / FASTLANE_KEY_ID（见 docs/design/ipad-udid-自动签名分发.md）"
  echo "▶ 个人模式：逐台注册新 UDID 到团队（企业免此步）"
  oldIFS=$IFS; IFS=','
  for u in $UDIDS; do
    [ -z "$u" ] && continue
    CI=1 FASTLANE_SKIP_UPDATE_CHECK=1 FASTLANE_TEAM_ID="${FASTLANE_TEAM_ID:-A3JP3VUZ78}" \
      fastlane register_and_sign udid:"$u" name:"user-$(printf '%s' "$u" | cut -c1-8)" \
      >/dev/null 2>&1 \
      || die "注册失败：$u（确认 API key/Issuer/证书，见 docs/design/ipad-udid-自动签名分发.md）"
  done
  IFS=$oldIFS
  echo "▶ 重新签名（xcodebuild，含新登记设备）"
  SIGN_MODE=personal sh scripts/package-wda-ipa.sh
fi

OUT="dist/wda.ipa"
[ -f "$OUT" ] || die "未产出 $OUT"
echo "输出：$OUT  sha256=$(shasum -a 256 "$OUT" | cut -c1-16)"

# 自动上传到云平台 /api/wda/package（可配置 URL/token；未配 token 只打包不上传）
WDA_UPLOAD_URL="${WDA_UPLOAD_URL:-https://us.hsddns.com/api/wda/package}"
[ -n "${WDA_API_TOKEN:-}" ] || die "上传需要 WDA_API_TOKEN（平台发布者 token）"
echo "▶ 上传到平台 $WDA_UPLOAD_URL"
curl -fsS -X POST \
  -H "Authorization: Bearer $WDA_API_TOKEN" \
  -F "file=@$OUT" -F "sign_mode=$MODE" -F "authorized_udids=$UDIDS" \
  "$WDA_UPLOAD_URL" || die "上传失败（平台需可达且 token 有效）"

echo "✅ 完成：账号类型=$MODE；网关收到 wda:config 后自动替换 state/wda.ipa，下次激活用新包，不影响运行。"
