# 2026-08-22 USB 整表拔走后必须拆隧道

手机都拔到 Windows 后，Mac 仍每 40s 打 `usb discovery empty with active tunnels, skip reconcile`，iproxy 不退，云上报还当 USB 在线。平台继续把群发打到这台已空的 Mac，Windows 收不到。

拆除改为与单台拔线相同：连续 2 轮 USB 列表没有就拆。单次 ioreg 空结果仍保留隧道。

```bash
go test ./internal/gateway/ -count=1 -run TestUSBTunnelsToDrop
```
