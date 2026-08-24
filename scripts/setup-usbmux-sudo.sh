#!/bin/bash
# usbmux 无线保活授权：允许网关免密重启系统 usbmuxd（`sudo -n /usr/bin/killall usbmuxd`）。
#
# 背景：让「USB 已接但缺 usbmux Network 条目」的设备自动进入 ConnectionType=Network，
#       需要重启系统 usbmuxd（Apple 的 launchd 守护进程会自动重新拉起）。
#       普通用户无权 killall usbmuxd，故用本脚本写入一条极窄 sudoers 规则。
#
# 规则仅允许 `killall usbmuxd`（带参数匹配），不影响 killall 其他进程，也不开放任意命令。
#
# 用法：
#   交互（需管理员）：sudo sh scripts/setup-usbmux-sudo.sh
#   Web 页面「立即修复」首次触发时，也可以走 osascript 管理员授权（脚本可省）。
set -euo pipefail
cd "$(dirname "$0")/.."

TARGET_USER="${SUDO_USER:-$(stat -f '%Su' /dev/console 2>/dev/null || whoami)}"
GROUP="wda-gateway"
RULE_FILE="/etc/sudoers.d/wda-gateway-usbmux"
KILL="/usr/bin/killall usbmuxd"

DS="dscl ."
if [ "$(id -u)" != "0" ]; then DS="sudo dscl ."; fi
if ! $DS -read "/Groups/$GROUP" >/dev/null 2>&1; then
  $DS -create "/Groups/$GROUP" >/dev/null 2>&1 || true
  echo "✓ 已创建权限组 ${GROUP}"
fi
if ! $DS -read "/Groups/$GROUP" GroupMembership 2>/dev/null | grep -qw "$TARGET_USER"; then
  $DS -append "/Groups/$GROUP" GroupMembership "$TARGET_USER" >/dev/null 2>&1 || true
  echo "✓ 已把 ${TARGET_USER} 加入权限组 ${GROUP}"
fi

{
  echo "%${GROUP} ALL=(root) NOPASSWD: ${KILL}"
  echo "${TARGET_USER} ALL=(root) NOPASSWD: ${KILL}"
} > "$RULE_FILE"
chmod 440 "$RULE_FILE"

echo "✓ sudoers 已配置（仅放行：${KILL}）："
grep -v '^$' "$RULE_FILE" | sed 's/^/    /'
echo "  撤销授权：sudo rm -f ${RULE_FILE}"
echo "✓ 完成：网关现在可免密重启 usbmuxd 以触发无线调试网络发现"
