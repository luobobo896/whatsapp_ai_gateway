#!/bin/bash
# WDA Farm Gateway 一键安装（新 Mac 部署用）：
#   1) 安装 easytier-core/cli 二进制（v2.6.4 macOS aarch64，缺省自动下载）
#   2) 创建权限组 wda-gateway 并把当前用户加入组
#   3) 配置 sudoers：%wda-gateway 组规则（长期管理）+ 当前用户规则（当前会话立即生效）
#   4) 验证免密
# 说明：easytier 节点开启虚拟网卡（TUN）需要 root，故需授权；本脚本幂等，可重复执行。
# 用法：sudo sh scripts/install.sh
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT="$(pwd)"
TARGET_USER="${SUDO_USER:-$(stat -f '%Su' /dev/console 2>/dev/null || whoami)}"
GROUP="wda-gateway"
EASYTIER_VER="v2.6.4"
ZIP="easytier-macos-aarch64-${EASYTIER_VER}.zip"
URL="https://github.com/EasyTier/EasyTier/releases/download/${EASYTIER_VER}/${ZIP}"
TOOLS="$ROOT/tools/easytier"
CORE="$TOOLS/easytier-core"
CLI="$TOOLS/easytier-cli"
RULE_FILE="/etc/sudoers.d/wda-gateway-easytier"

echo "▶ [1/4] easytier 二进制（目标：${TOOLS}）"
if [ ! -x "$CORE" ] || [ ! -x "$CLI" ]; then
  mkdir -p "$TOOLS"
  echo "   下载 ${EASYTIER_VER}..."
  cd /tmp
  curl -fsSL -o "$ZIP" "$URL"
  python3 -c "import zipfile,sys; zipfile.ZipFile(sys.argv[1]).extractall('/tmp/et-install')" "$ZIP"
  install -m 0700 "/tmp/et-install/easytier-macos-aarch64/easytier-core" "$CORE"
  install -m 0700 "/tmp/et-install/easytier-macos-aarch64/easytier-cli" "$CLI"
  rm -rf /tmp/et-install "$ZIP"
fi
if [ "$(id -u)" = "0" ]; then
  chown "$TARGET_USER" "$CORE" "$CLI" 2>/dev/null || true
fi
echo "   easytier-core: $("$CORE" --version 2>/dev/null || sudo -u "$TARGET_USER" "$CORE" --version 2>/dev/null)"
echo "   sha256: $(shasum -a 256 "$CORE" | awk '{print $1}')"

echo "▶ [2/4] 权限组 $GROUP + 用户 $TARGET_USER"
DS="dscl ."
if [ "$(id -u)" != "0" ]; then DS="sudo dscl ."; fi
if ! $DS -read "/Groups/$GROUP" >/dev/null 2>&1; then
  $DS -create "/Groups/$GROUP" >/dev/null 2>&1 || true
  echo "   已创建权限组 $GROUP"
fi
if ! $DS -read "/Groups/$GROUP" GroupMembership 2>/dev/null | grep -qw "$TARGET_USER"; then
  $DS -append "/Groups/$GROUP" GroupMembership "$TARGET_USER" >/dev/null 2>&1 || true
  echo "   已把 $TARGET_USER 加入权限组 $GROUP"
else
  echo "   $TARGET_USER 已在权限组 $GROUP"
fi

echo "▶ [3/4] sudoers（%${GROUP} 组规则 + ${TARGET_USER} 用户规则）"
{
  echo "%$GROUP ALL=(root) NOPASSWD: $CORE"
  echo "%$GROUP ALL=(root) NOPASSWD: /usr/bin/pkill -f easytier-core*"
  echo "$TARGET_USER ALL=(root) NOPASSWD: $CORE"
  echo "$TARGET_USER ALL=(root) NOPASSWD: /usr/bin/pkill -f easytier-core*"
} > "$RULE_FILE"
chmod 440 "$RULE_FILE"
grep -v '^$' "$RULE_FILE" | sed 's/^/    /'

echo "▶ [4/4] 验证免密"
if sudo -u "$TARGET_USER" sudo -n "$CORE" --version >/dev/null 2>&1; then
  echo "✓ 验证通过：$TARGET_USER 可免密运行 easytier-core（开启虚拟网卡无需再输密码）"
else
  echo "⚠ 当前会话未识别授权（macOS 组缓存），请重新登录一次；或检查 $RULE_FILE" >&2
  exit 1
fi

echo ""
echo "✅ 安装完成。"
echo "   - 新增网关用户：sudo dscl . -append /Groups/$GROUP GroupMembership <用户名>"
echo "   - 启动网关：cd $ROOT && ./run.sh"
echo "   - 虚拟网卡授权仅在首次安装时配置，之后启动/平台下发重启均免密"
