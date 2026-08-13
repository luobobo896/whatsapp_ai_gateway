#!/bin/bash
# easytier 虚拟网卡（TUN）授权：以「权限组」方式管理，组内成员免密运行 easytier-core。
#   - 创建组 wda-gateway（不存在则建），把当前用户加入组
#   - sudoers 写 %wda-gateway 组规则（运行 easytier-core + 清理旧进程）
#   - 之后新增网关用户只需：sudo dscl . -append /Groups/wda-gateway GroupMembership <用户名>
# 用法：
#   交互：sudo sh scripts/setup-easytier-sudo.sh
#   GUI 授权（Web 页面点「启动」首次触发）：osascript ... with administrator privileges
# 脚本可能被 sudo / osascript(root) 执行；目标用户取 GUI 控制台用户（stat /dev/console）。
set -euo pipefail
cd "$(dirname "$0")/.."
CORE="$(pwd)/tools/easytier/easytier-core"
if [ ! -x "$CORE" ]; then
  echo "✗ easytier-core 不存在：$CORE" >&2
  exit 1
fi
TARGET_USER="${SUDO_USER:-$(stat -f '%Su' /dev/console 2>/dev/null || whoami)}"
GROUP="wda-gateway"
RULE_FILE="/etc/sudoers.d/wda-gateway-easytier"

# 1) 权限组：创建 + 把当前用户加入（root 直接 dscl；非 root 经 sudo）
DS="dscl ."
if [ "$(id -u)" != "0" ]; then DS="sudo dscl ."; fi
if ! $DS -read "/Groups/$GROUP" >/dev/null 2>&1; then
  $DS -create "/Groups/$GROUP" >/dev/null 2>&1 || true
  echo "✓ 已创建权限组 ${GROUP}"
fi
if ! $DS -read "/Groups/$GROUP" GroupMembership 2>/dev/null | grep -qw "$TARGET_USER"; then
  $DS -append "/Groups/$GROUP" GroupMembership "$TARGET_USER" >/dev/null 2>&1 || true
  echo "✓ 已把 $TARGET_USER 加入权限组 ${GROUP}"
fi

# 2) sudoers 规则：
#    - %wda-gateway 组规则：长期管理，新增用户只需加入组（sudo dscl . -append /Groups/wda-gateway GroupMembership <用户>）
#    - ${TARGET_USER} 用户规则：保证当前已登录会话立即生效（macOS 组缓存需重新登录才识别新成员）
{
  echo "%$GROUP ALL=(root) NOPASSWD: $CORE"
  echo "%$GROUP ALL=(root) NOPASSWD: /usr/bin/pkill -f easytier-core*"
  echo "${TARGET_USER} ALL=(root) NOPASSWD: $CORE"
  echo "${TARGET_USER} ALL=(root) NOPASSWD: /usr/bin/pkill -f easytier-core*"
} > "$RULE_FILE"
chmod 440 "$RULE_FILE"
echo "✓ sudoers 已配置（权限组 %${GROUP} + 当前用户 ${TARGET_USER}）："
grep -v '^$' "$RULE_FILE" | sed 's/^/    /'
echo "  提示：新增网关用户只需执行  sudo dscl . -append /Groups/wda-gateway GroupMembership <用户名>"
echo "✓ 授权完成（开启虚拟网卡 TUN 所需 root 权限）"
