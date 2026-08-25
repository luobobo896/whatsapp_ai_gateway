# 网关发现 usbmux Network 设备

日期：2026-08-25

## 问题

拔掉 USB 后 `idevice_id -l` 为空，两台真机只出现在 `idevice_id -n` / `idevice_id -l -n` 的 `(Network)` 行。网关 `Discover()` / 设备列表原先只读 `-l`，无线设备进不了列表，也无法点 Network 激活。

## 改动

- `Discover()` 合并 USB + Network + `devicectl`
- `USBUDIDs()` 仍只返回 USB，避免把无线设备当成 USB 隧道
- 设备列表 / 云端：Network-only 显示 `conn_type=wifi`，且不算掉线

## 复验

```bash
idevice_id -l          # 拔线时应为空
idevice_id -l -n       # 应有 UDID (Network)
go test ./internal/gateway -count=1 -timeout 90s \
  -run 'TestParseIdeviceIDLines|TestMergeDiscovered|TestMuxPresence|TestDiscoverIncludesLiveNetworkUDIDs|TestDeviceListHidesOfflineConfigured|TestDeviceListReport'
```

安装包内的网关需重新打包后才会带上这次发现逻辑。
