#!/bin/bash
# WDA 自动授权守护：轮询平台设备授权列表，发现未授权(新机)就自动注册+重签+上传。
# 运行一次常驻（可 crontab/后台）；需要 WDA_API_TOKEN（signing.env 里有）。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

SIGNING_ENV="${SIGNING_ENV:-$ROOT/scripts/signing.env}"
if [ -f "$SIGNING_ENV" ]; then
  set -a
  # shellcheck disable=SC1090
  . "$SIGNING_ENV"
  set +a
fi

MODE="${MODE:-personal}"
INTERVAL="${WATCH_INTERVAL:-300}"
DEVICES_URL="${WDA_DEVICES_URL:-https://us.hsddns.com/api/wda/devices}"

unauthorized() {
  [ -n "${WDA_API_TOKEN:-}" ] || { echo "⚠ 未设置 WDA_API_TOKEN，无法轮询" >&2; echo ""; return; }
  curl -fsS --max-time 20 -H "Authorization: Bearer $WDA_API_TOKEN" "$DEVICES_URL" 2>/dev/null \
    | python3 -c 'import sys,json
try:
  d=json.load(sys.stdin)
except Exception:
  sys.exit(0)
print(",".join(x["udid"] for x in d.get("devices",[]) if not x.get("authorized")))' 2>/dev/null || echo ""
}

echo "▶ WDA 自动授权守护启动：mode=$MODE interval=${INTERVAL}s devices=$DEVICES_URL"
while true; do
  NEW="$(unauthorized)"
  if [ -n "$NEW" ]; then
    echo "发现未授权设备：$NEW → 注册+重签+上传"
    sh scripts/resign-wda-for-udids.sh --mode "$MODE" --udids "$NEW" \
      || echo "⚠ 本轮失败（确认 Apple/网络），下轮重试"
  else
    echo "暂无未授权设备，等待 ${INTERVAL}s"
  fi
  sleep "$INTERVAL"
done
