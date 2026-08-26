#!/bin/bash
# 上传客户端安装包到云平台 /api/releases/publish（同平台覆盖，只留最新）。
# 用法：sh scripts/upload-release.sh <文件> <darwin|windows> <arch> <version>
# 需要 WDA_PUBLISHER_TOKEN（机器发布鉴权）。
set -euo pipefail
FILE="${1:?用法: upload-release.sh <file> <platform> <arch> <version>}"
PLATFORM="${2:?platform(darwin|windows)}"
ARCH="${3:-}"
VERSION="${4:-dev}"
RELEASE_PUBLISH_URL="${RELEASE_PUBLISH_URL:-https://us.hsddns.com/api/releases/publish}"

[ -f "$FILE" ] || { echo "✗ 文件不存在: $FILE" >&2; exit 1; }
[ -n "${WDA_PUBLISHER_TOKEN:-}" ] || { echo "⚠ 未设置 WDA_PUBLISHER_TOKEN，跳过上传" >&2; exit 0; }

echo "▶ 上传 $PLATFORM/$ARCH v$VERSION -> $RELEASE_PUBLISH_URL"
if curl -fsS -X POST \
  -H "Authorization: Bearer $WDA_PUBLISHER_TOKEN" \
  -F "file=@$FILE" -F "version=$VERSION" -F "platform=$PLATFORM" -F "arch=$ARCH" \
  "$RELEASE_PUBLISH_URL"; then
  echo "✅ 已上传（同平台已覆盖为最新）"
else
  echo "✗ 上传失败（检查 token / 网络 / 平台可达）" >&2
  exit 1
fi
