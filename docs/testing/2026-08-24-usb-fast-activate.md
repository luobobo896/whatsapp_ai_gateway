# 2026-08-24 USB 最短激活（已撤销）

- 日期：2026-08-24
- 状态：**已撤销**。USB 最短路径能很快出现 Automation Running，但 XCTest 绑死 USB，拔线即死，违反核心需求。

## 为何撤销

同晚对照：

- Plus-2 `4886579a…`：`wifi-runwda via=Network`，拔 USB 后 `http://192.168.10.237:8100/status` 仍 ready。
- iPhone 7 `5060c403…`：等 45s 后 USB 回退，`via=USB`，拔线 testmanagerd EOF，Automation Running 消失。

产品要求与 Plus-2 一致：激活成功后必须能拔 USB，改走 Wi-Fi。

现行路径见 [2026-08-24-network-unplug.md](2026-08-24-network-unplug.md)。
