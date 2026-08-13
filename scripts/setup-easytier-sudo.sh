#!/bin/bash
# 一次性授权：为「开启虚拟网卡（TUN）」放行免密运行本网关的 easytier-core + 清理旧进程。
# 用法：
#   交互：sudo sh scripts/setup-easytier-sudo.sh
#   GUI 授权（Web 页面点「启动」时自动触发）：osascript -e 'do shell script "sh <本脚本>" with administrator privileges'
# 脚本可能被 sudo / osascript(root) 执行，目标用户取 GUI 控制台用户（Mac: stat /dev/console），
# 避免在 root 环境下误写 root 自己的规则。
set -euo pipefail
cd "$(dirname "$0")/.."
CORE="$(pwd)/tools/easytier/easytier-core"
if [ ! -x "$CORE" ]; then
  echo "✗ easytier-core 不存在：$CORE" >&2
  exit 1
fi
TARGET_USER="${SUDO_USER:-$(stat -f '%Su' /dev/console 2>/dev/null || whoami)}"
RULE_FILE="/etc/sudoers.d/wda-gateway-easytier"
{
  echo "${TARGET_USER} ALL=(root) NOPASSWD: ${CORE}"
  echo "${TARGET_USER} ALL=(root) NOPASSWD: /usr/bin/pkill -f easytier-core*"
} > "$RULE_FILE"
chmod 440 "$RULE_FILE"
echo "✓ sudoers 已配置（用户 ${TARGET_USER}）："
grep -v '^$' "$RULE_FILE" | sed 's/^/    /'
if sudo -n "$CORE" --version >/dev/null 2>&1; then
  echo "✓ 免密验证通过：$(sudo -n "$CORE" --version)"
else
  echo "✗ 免密验证失败" >&2
  exit 1
fi
