#!/bin/bash
# 一次性授权：允许当前用户免密运行本网关的 easytier-core（仅此二进制，最小权限）。
# 原因：macOS 上 easytier 节点要有虚 IP（ipv4 非空）必须创建 TUN 设备，需要 root。
# 用法：sudo sh scripts/setup-easytier-sudo.sh   （首次执行需输入一次密码）
set -euo pipefail
cd "$(dirname "$0")/.."
CORE="$(pwd)/tools/easytier/easytier-core"
if [ ! -x "$CORE" ]; then
  echo "✗ easytier-core 不存在：$CORE" >&2
  exit 1
fi
RULE_FILE="/etc/sudoers.d/wda-gateway-easytier"
# 需要 root 的两件事：运行 easytier-core（建 TUN 绑虚 IP）+ pkill 旧 easytier 进程（重启生效）。
# 注意：脚本可能被 sudo/osascript 以 root 执行，whoami 是 root；需显式写当前调用用户。
TARGET_USER="${SUDO_USER:-$(whoami)}"
RULE1="${TARGET_USER} ALL=(root) NOPASSWD: ${CORE}"
RULE2="${TARGET_USER} ALL=(root) NOPASSWD: /usr/bin/pkill -f easytier-core*"
{
  echo "$RULE1"
  echo "$RULE2"
} > "$RULE_FILE"
chmod 440 "$RULE_FILE"
echo "✓ sudoers 已配置："
grep -v '^$' "$RULE_FILE" | sed 's/^/    /'
# 验证免密
if sudo -n "$CORE" --version >/dev/null 2>&1; then
  echo "✓ 免密验证通过：$(sudo -n "$CORE" --version)"
else
  echo "✗ 免密验证失败，请检查 $RULE_FILE" >&2
  exit 1
fi
