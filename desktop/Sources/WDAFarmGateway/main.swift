import AppKit
import WebKit

// WDA Farm Gateway 桌面壳：拉起内嵌 gateway 子进程（0.0.0.0:8300），
// 主窗口 WKWebView 加载 http://127.0.0.1:8300/（与网页版同一页面，功能一致），
// 局域网其他设备仍可经 http://<Mac-IP>:8300 访问。
//
// 目录约定：
//   只读资源（gateway 二进制、static、WhatsAppDeviceAgent、tools、bin、scripts）
//     → Bundle.main.resourceURL
//   可写状态（gateway.db、data、logs）
//     → ~/Library/Application Support/WDAFarmGateway

let appURL = URL(fileURLWithPath: CommandLine.arguments.first ?? "WDAFarmGateway")
let appSupport = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
    .appendingPathComponent("WDAFarmGateway", isDirectory: true)
// 默认 8300（与源码运行形态一致）；GATEWAY_PORT 仅用于并行测试等场景
let listenPort = Int(ProcessInfo.processInfo.environment["GATEWAY_PORT"] ?? "") ?? 8300

func stateDir(_ sub: String = "") -> URL {
    let base = appSupport
    try? FileManager.default.createDirectory(at: base, withIntermediateDirectories: true)
    return sub.isEmpty ? base : base.appendingPathComponent(sub, isDirectory: true)
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    var statusItem: NSStatusItem!
    var window: NSWindow?
    var webView: WKWebView?
    var gateway = GatewayProcess()
    var readyPoller: Timer?
    var lastRestart = Date.distantPast

    func applicationDidFinishLaunching(_ notification: Notification) {
        guard ensureSingleInstance() else {
            NSApp.terminate(nil)
            return
        }
        setupMainMenu()
        setupStatusItem()
        setupWindow(loading: true)
        NSApp.activate(ignoringOtherApps: true)

        prepareStateDirs()
        gateway.onExit = { [weak self] unexpected in
            DispatchQueue.main.async { self?.gatewayDidExit(unexpected: unexpected) }
        }
        gateway.start()
        pollUntilReady()
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        false // 关窗口=隐藏，退出走菜单栏「退出」
    }

    func applicationWillTerminate(_ notification: Notification) {
        readyPoller?.invalidate()
        gateway.terminateGracefully()
    }

    // MARK: - 单实例（同 bundle id 已在运行则激活它并退出）

    private func ensureSingleInstance() -> Bool {
        guard let bid = Bundle.main.bundleIdentifier, !bid.isEmpty else { return true }
        let others = NSRunningApplication.runningApplications(withBundleIdentifier: bid).filter { $0 != NSRunningApplication.current }
        guard let running = others.first else { return true }
        running.activate(options: [.activateAllWindows])
        return false
    }

    // MARK: - 主菜单（含 Edit 菜单：WKWebView 的 Cmd+C/V/X/A 依赖标准编辑菜单项走 responder chain，
    // 纯代码建的窗口没有主菜单时输入框无法复制粘贴）

    private func setupMainMenu() {
        let main = NSMenu()

        let appMenu = NSMenuItem(title: "WDA Farm Gateway", action: nil, keyEquivalent: "")
        let appSub = NSMenu()
        appSub.addItem(withTitle: "退出", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
        appMenu.submenu = appSub
        main.addItem(appMenu)

        let editMenu = NSMenuItem(title: "编辑", action: nil, keyEquivalent: "")
        let editSub = NSMenu()
        editSub.addItem(withTitle: "剪切", action: #selector(NSText.cut(_:)), keyEquivalent: "x")
        editSub.addItem(withTitle: "拷贝", action: #selector(NSText.copy(_:)), keyEquivalent: "c")
        editSub.addItem(withTitle: "粘贴", action: #selector(NSText.paste(_:)), keyEquivalent: "v")
        editSub.addItem(withTitle: "全选", action: #selector(NSText.selectAll(_:)), keyEquivalent: "a")
        editMenu.submenu = editSub
        main.addItem(editMenu)

        let windowMenu = NSMenuItem(title: "窗口", action: nil, keyEquivalent: "")
        let windowSub = NSMenu()
        windowSub.addItem(withTitle: "关闭", action: #selector(NSWindow.performClose(_:)), keyEquivalent: "w")
        windowSub.addItem(withTitle: "最小化", action: #selector(NSWindow.performMiniaturize(_:)), keyEquivalent: "m")
        windowMenu.submenu = windowSub
        main.addItem(windowMenu)

        NSApp.mainMenu = main
    }

    // MARK: - 状态目录

    private func prepareStateDirs() {
        for sub in ["data", "logs"] {
            try? FileManager.default.createDirectory(at: stateDir(sub), withIntermediateDirectories: true)
        }
    }

    // MARK: - 子进程生命周期

    private func pollUntilReady() {
        var attempts = 0
        readyPoller = Timer.scheduledTimer(withTimeInterval: 0.5, repeats: true) { [weak self] t in
            guard let self else { return t.invalidate() }
            attempts += 1
            var req = URLRequest(url: URL(string: "http://127.0.0.1:\(listenPort)/api/cloud")!)
            req.timeoutInterval = 2
            URLSession.shared.dataTask(with: req) { _, response, _ in
                let ok = (response as? HTTPURLResponse)?.statusCode == 200 || (response as? HTTPURLResponse)?.statusCode == 401
                DispatchQueue.main.async {
                    if ok {
                        t.invalidate()
                        self.loadPage()
                    } else if attempts > 120 {
                        t.invalidate()
                        self.gatewayDidExit(unexpected: true)
                    }
                }
            }.resume()
        }
    }

    private func gatewayDidExit(unexpected: Bool) {
        statusItem.button?.title = "⚠︎ 网关已停止"
        guard unexpected, Date().timeIntervalSince(lastRestart) > 10 else { return }
        lastRestart = Date()
        NSLog("gateway exited unexpectedly, restarting")
        gateway.start()
        pollUntilReady()
    }

    // MARK: - UI

    private func setupStatusItem() {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        statusItem.button?.title = "◉ 网关"
        let menu = NSMenu()
        menu.addItem(withTitle: "打开管理页", action: #selector(openPage), keyEquivalent: "o")
        menu.addItem(withTitle: "复制局域网访问地址", action: #selector(copyLANAddress), keyEquivalent: "c")
        menu.addItem(.separator())
        menu.addItem(withTitle: "重启后台服务", action: #selector(restartGateway), keyEquivalent: "r")
        menu.addItem(withTitle: "打开数据目录", action: #selector(openStateDir), keyEquivalent: "")
        menu.addItem(withTitle: "打开日志", action: #selector(openLogs), keyEquivalent: "")
        menu.addItem(.separator())
        menu.addItem(withTitle: "退出", action: #selector(quit), keyEquivalent: "q")
        for item in menu.items { item.target = self }
        statusItem.menu = menu
    }

    private func setupWindow(loading: Bool) {
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .default() // 持久化 cookie/会话
        let wv = WKWebView(frame: NSRect(x: 0, y: 0, width: 1280, height: 860), configuration: config)
        if loading {
            wv.loadHTMLString("<html><body style=\"font-family:-apple-system;background:#111;color:#eee;display:flex;align-items:center;justify-content:center;height:100vh\"><div><h2>WDA Farm Gateway</h2><p>正在启动后台服务…</p></div></body></html>", baseURL: nil)
        }
        let win = NSWindow(contentViewController: NSViewController())
        win.contentViewController?.view = wv
        win.title = "WDA Farm Gateway"
        win.styleMask.insert(.closable); win.styleMask.insert(.miniaturizable); win.styleMask.insert(.resizable)
        win.makeKeyAndOrderFront(nil)
        window = win
        webView = wv
    }

    private func loadPage() {
        guard let webView else { return }
        statusItem.button?.title = "◉ 网关"
        webView.load(URLRequest(url: URL(string: "http://127.0.0.1:\(listenPort)/")!))
    }

    // MARK: - 菜单动作

    @objc private func openPage() {
        window?.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
        if gateway.isRunning { loadPage() } else { restartGateway() }
    }

    @objc private func copyLANAddress() {
        let addr = Network.lanIPv4() ?? "127.0.0.1"
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString("http://\(addr):\(listenPort)", forType: .string)
    }

    @objc private func restartGateway() {
        gateway.terminateGracefully()
        gateway.start()
        pollUntilReady()
    }

    @objc private func openStateDir() { NSWorkspace.shared.open(stateDir()) }
    @objc private func openLogs() { NSWorkspace.shared.open(stateDir("logs")) }
    @objc private func quit() { NSApp.terminate(nil) }
}

enum Network {
    static func interfaceIPv4s() -> [String: String] {
        var result: [String: String] = [:]
        var ifaddr: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&ifaddr) == 0, let first = ifaddr else { return result }
        defer { freeifaddrs(ifaddr) }
        var ptr: UnsafeMutablePointer<ifaddrs>? = first
        while let p = ptr {
            let ifa = p.pointee
            if let sa = ifa.ifa_addr, sa.pointee.sa_family == UInt8(AF_INET) {
                var addr = sockaddr_in()
                memcpy(&addr, sa, MemoryLayout<sockaddr_in>.size)
                var buf = [CChar](repeating: 0, count: Int(INET_ADDRSTRLEN))
                if inet_ntop(AF_INET, &addr.sin_addr, &buf, socklen_t(INET_ADDRSTRLEN)) != nil {
                    let ip = String(cString: buf)
                    let name = String(cString: ifa.ifa_name)
                    if !ip.hasPrefix("127.") { result[name] = ip }
                }
            }
            ptr = p.pointee.ifa_next
        }
        return result
    }

    static func lanIPv4() -> String? {
        let ifs = interfaceIPv4s()
        return ifs["en0"] ?? ifs.values.first
    }
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.regular)
app.run()
