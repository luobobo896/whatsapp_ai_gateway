#!/bin/bash
# iOS 设备切换为 Network（Wi-Fi）连接 —— libimobiledevice 一键工具
#
# 功能：
#   1) 补全工具链：缺 idevicepair 时自动从 Homebrew bottle 提取、修复链接并安装
#   2) 枚举 USB / Network 设备并验证每台设备的 USB + Network 双通道配对
#   3) 引导切换：拔掉 USB 线后 ConnectionType 即变为 Network，并给出验证命令
#
# 原理：ConnectionType 是 usbmuxd 依据物理连接自动决定的只读属性，libimobiledevice
#       无法直接"写"。Wi-Fi 配对（idevicepair -n validate）就绪后，拔线即自动切 Network。
#
# 用法：sh scripts/ios-network-pair.sh
# 可选：SKIP_INSTALL=1 只做验证不安装 idevicepair
# 幂等，可重复执行。
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT="$(pwd)"

# ---------- 工具与路径 ----------
if [ -d /opt/homebrew ]; then PREFIX="/opt/homebrew"; else PREFIX="/usr/local"; fi
export PATH="$PREFIX/bin:$PATH"
BOTTLE_VERSION="1.4.0"                       # libimobiledevice 版本（与 formula stable 一致）
TOOL="idevicepair"
DEST="$PREFIX/bin/$TOOL"
TMP="/tmp/libimobiledevice-bottle"
ARCH="$(uname -m)"

echo "▶ [1/4] libimobiledevice 工具链"

# 1) idevice_id / ideviceinfo / iproxy 检查
MISSING=0
for t in idevice_id ideviceinfo iproxy; do
  if ! command -v "$t" >/dev/null 2>&1; then
    echo "  ✗ 缺 $t（请先安装 libimobiledevice：brew install libimobiledevice）" >&2
    MISSING=1
  fi
done
if [ "$MISSING" = "1" ]; then exit 1; fi
echo "  ✓ idevice_id / ideviceinfo / iproxy 可用"

# 2) idevicepair：缺失则自动安装
if command -v "$TOOL" >/dev/null 2>&1; then
  echo "  ✓ $TOOL 已存在：$(command -v "$TOOL") ($("$TOOL" --version 2>/dev/null | head -1))"
elif [ "${SKIP_INSTALL:-0}" = "1" ]; then
  echo "  ✗ 缺 $TOOL 且 SKIP_INSTALL=1，跳过安装" >&2; exit 1
else
  echo "  缺 $TOOL，从 Homebrew bottle 提取安装..."
  echo "    (bottle: libimobiledevice $BOTTLE_VERSION, $ARCH)"

  # 2.1 取 bottle 下载 URL（formulae.brew.sh API）
  API_JSON="/tmp/libimobiledevice-api.json"
  curl --max-time 30 -fsSL "https://formulae.brew.sh/api/formula/libimobiledevice.json" -o "$API_JSON" \
    || { echo "  ✗ 获取 formula API 失败（需联网）" >&2; exit 1; }
  # 选择匹配的 bottle key：arm64 优先当前 macOS，其次任意 arm64_*；x86_64 用 sonoma
  OSVER="$(sw_vers -productVersion 2>/dev/null || echo 0)"
  KEY=""
  if [ "$ARCH" = "arm64" ]; then
    for cand in "arm64_${OSVER%%.*}" ; do :; done # 占位，真实匹配走下面
    for cand in "arm64_tahoe" "arm64_sequoia" "arm64_sonoma"; do
      if python3 -c "import json,sys;d=json.load(open('$API_JSON'));f=d['bottle']['stable']['files'];sys.exit(0 if '$cand' in f else 1)" 2>/dev/null; then
        KEY="$cand"; break
      fi
    done
    [ -z "$KEY" ] && KEY="$(python3 -c "import json;d=json.load(open('$API_JSON'));f=d['bottle']['stable']['files'];print(next((k for k in f if k.startswith('arm64_')),''))" 2>/dev/null || true)"
  else
    KEY="sonoma"
  fi
  URL="$(python3 -c "import json,sys;d=json.load(open('$API_JSON'));f=d['bottle']['stable']['files'];print(f['$KEY']['url'] if '$KEY' in f else '')" 2>/dev/null || true)"
  if [ -z "$URL" ]; then echo "  ✗ 无法确定 bottle 下载地址" >&2; exit 1; fi
  echo "  ✓ bottle key=$KEY"

  # 2.2 下载（ghcr.io 需匿名 token）
  TOKEN="$(curl --max-time 30 -fsSL "https://ghcr.io/token?scope=repository:homebrew/core/libimobiledevice:pull&service=ghcr.io" \
    | python3 -c "import sys,json;print(json.load(sys.stdin)['token'])")"
  curl --max-time 300 -L -fsSL -H "Authorization: Bearer $TOKEN" "$URL" -o /tmp/libimobiledevice-bottle.tar.gz \
    || { echo "  ✗ 下载 bottle 失败" >&2; exit 1; }
  rm -rf "$TMP" && mkdir -p "$TMP"
  tar -xzf /tmp/libimobiledevice-bottle.tar.gz -C "$TMP"
  SRC="$(find "$TMP" -type f -name "$TOOL" | head -1)"
  [ -n "$SRC" ] || { echo "  ✗ bottle 中未找到 $TOOL" >&2; exit 1; }

  # 2.3 修复链接：@@HOMEBREW_CELLAR@@ / @@HOMEBREW_PREFIX@@ 占位符 → 本机真实路径
  cp "$SRC" "$DEST"; chmod 755 "$DEST"
  CELLAR_LIB="$(ls -d "$PREFIX/Cellar/libimobiledevice"/*/ 2>/dev/null | head -1 || true)"
  if [ -n "$CELLAR_LIB" ]; then
    LIB_IMODE="$CELLAR_LIB/lib/libimobiledevice-1.0.6.dylib"
  else
    LIB_IMODE="$PREFIX/Cellar/libimobiledevice/$BOTTLE_VERSION/lib/libimobiledevice-1.0.6.dylib"
    echo "  ⚠ 本机无 Cellar libimobiledevice，链接指向 $BOTTLE_VERSION（缺库时需 DYLD_LIBRARY_PATH 或 brew 安装）"
  fi
  change() { install_name_tool -change "$1" "$2" "$DEST" 2>/dev/null || true; }
  change "@@HOMEBREW_CELLAR@@/libimobiledevice/$BOTTLE_VERSION/lib/libimobiledevice-1.0.6.dylib" "$LIB_IMODE"
  for d in libusbmuxd/lib/libusbmuxd-2.0.7.dylib \
           libimobiledevice-glue/lib/libimobiledevice-glue-1.0.0.dylib \
           libplist/lib/libplist-2.0.4.dylib \
           openssl@3/lib/libssl.3.dylib \
           openssl@3/lib/libcrypto.3.dylib; do
    if [ -d "$PREFIX/opt/${d%/*}" ]; then REAL="$PREFIX/opt/$d"; else REAL="$PREFIX/lib/${d##*/}"; fi
    change "@@HOMEBREW_PREFIX@@/opt/$d" "$REAL"
  done
  codesign --force -s - "$DEST" >/dev/null 2>&1 || true
  echo "  ✓ 已安装 $DEST（占位符已修复，ad-hoc 重签）"
fi

echo "▶ [2/4] 设备枚举"
ALL_DEVICES="$(idevice_id -l 2>/dev/null || true)"
NET_DEVICES="$(idevice_id -l -n 2>/dev/null || true)"
if [ -z "$ALL_DEVICES" ] && [ -z "$NET_DEVICES" ]; then
  echo "  ⚠ 未发现任何 iOS 设备（USB 或 Wi-Fi）。请插线或确保设备与 Mac 同一 Wi-Fi。"
  exit 0
fi
[ -n "$ALL_DEVICES" ] && { echo "  -- 全部设备 --"; echo "$ALL_DEVICES" | sed 's/^/    /'; }
echo "  -- 连接类型总览 (USB / Network) --"
echo "$NET_DEVICES" | sed 's/^/    /'

echo "▶ [3/4] 配对验证"
UDIDS="$( { echo "$ALL_DEVICES"; echo "$NET_DEVICES"; } | awk '{print $1}' | sort -u )"
[ -n "$UDIDS" ] || exit 0
printf "    %-40s %-8s %-10s %s\n" "UDID" "USB" "Network" "DeviceName"
while IFS= read -r udid; do
  [ -n "$udid" ] || continue
  usb="$(idevicepair -u "$udid" validate 2>/dev/null | head -1 | grep -q SUCCESS && echo ✓ || echo -)"
  net="$(idevicepair -n -u "$udid" validate 2>/dev/null | head -1 | grep -q SUCCESS && echo ✓ || echo -)"
  name="$(ideviceinfo -u "$udid" -n -k DeviceName 2>/dev/null | head -1 || ideviceinfo -u "$udid" -k DeviceName 2>/dev/null | head -1 || true)"
  printf "    %-40s %-8s %-10s %s\n" "$udid" "$usb" "$net" "$name"
done <<< "$UDIDS"

echo "▶ [4/4] Network 切换引导"
HAS_USB="$(echo "$NET_DEVICES" | grep -c '(USB)' || true)"
HAS_NET="$(echo "$NET_DEVICES" | grep -c '(Network)' || true)"
if [ "$HAS_NET" -gt 0 ]; then
  echo "  ✓ 已有 $HAS_NET 条 Network 连接。"
  if [ "$HAS_USB" -gt 0 ]; then
    echo "  → 目标设备同时存在 USB + Network。拔掉 USB 线后 ConnectionType 即变 Network："
    echo "      idevice_id -l -n | grep '(Network)'"
  else
    echo "  ✓ 设备已全部为 Network 连接（ConnectionType=Network）。"
  fi
else
  echo "  ⚠ 无 Network 连接。请确认：设备与 Mac 同一 Wi-Fi；已在 iPhone『设置→通用→关于本机』信任本机（或 Finder 勾选『通过 Wi-Fi 显示此 iPhone』）。"
fi
echo "✅ 完成：scripts/ios-network-pair.sh"
