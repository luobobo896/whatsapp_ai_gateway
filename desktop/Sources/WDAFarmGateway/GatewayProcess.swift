import Foundation

// GatewayProcess：内嵌 gateway 二进制的子进程管理。
// 启动参数与源码运行形态一致（-state 指向用户可写目录，资源路径显式指向 bundle）；
// 退出时先 SIGTERM（gateway 侧有 15s 优雅停机）再兜底 SIGKILL。

enum GatewayProcessError: Error, CustomStringConvertible {
    case missingBinary(String)
    var description: String {
        if case .missingBinary(let p) = self { return "gateway 二进制缺失：\(p)" }
        return "unknown"
    }
}

final class GatewayProcess {
    private(set) var process: Process?
    var onExit: ((Bool) -> Void)? // 参数=是否意外退出（非用户主动停止）

    private(set) var manualStop = false
    private var logHandle: FileHandle?

    var isRunning: Bool { process?.isRunning ?? false }

    private var resourcesDir: URL {
        Bundle.main.resourceURL ?? appURL.deletingLastPathComponent()
    }

    private var gatewayBin: URL {
        // .app 形态：Contents/MacOS/gateway；swift run 调试形态：与壳同目录
        Bundle.main.url(forResource: "gateway", withExtension: nil)
            ?? appURL.deletingLastPathComponent().appendingPathComponent("gateway")
    }

    func start() {
        guard !isRunning else { return }
        cleanupStaleServices()
        let bin = gatewayBin
        guard FileManager.default.isExecutableFile(atPath: bin.path) else {
            shellLog("start: \(GatewayProcessError.missingBinary(bin.path))")
            onExit?(false)
            return
        }
        manualStop = false

        let logURL = stateDir("logs").appendingPathComponent("gateway.log")
        try? FileManager.default.createDirectory(at: stateDir("logs"), withIntermediateDirectories: true)
        if !FileManager.default.fileExists(atPath: logURL.path) {
            FileManager.default.createFile(atPath: logURL.path, contents: nil)
        }
        if let h = logHandle { try? h.close() }
        logHandle = FileHandle(forWritingAtPath: logURL.path)
        logHandle?.seekToEndOfFile()

        let p = Process()
        p.executableURL = bin
        p.arguments = [
            "-state", stateDir().path,
            "-project", resourcesDir.appendingPathComponent("WhatsAppDeviceAgent").path,
            "-static", resourcesDir.appendingPathComponent("static").path,
            "-listen", "0.0.0.0:\(listenPort)",
        ]
        var env = ProcessInfo.processInfo.environment
        let binDir = resourcesDir.appendingPathComponent("bin").path
        env["PATH"] = binDir + ":" + (env["PATH"] ?? "/usr/bin:/bin")
        env["WDA_GATEWAY_RESOURCES"] = resourcesDir.path
        // 包内 iproxy/ideviceinfo 为 Homebrew 原样二进制（不修改 LC，避免 dyld4 挂死），
        // 用包内 lib 强制解析其 dylib 依赖（DYLD_LIBRARY_PATH 优先于绝对路径 LC，已实证）。
        env["DYLD_LIBRARY_PATH"] = resourcesDir.appendingPathComponent("lib").path
        p.environment = env
        p.standardOutput = logHandle
        p.standardError = logHandle
        p.terminationHandler = { [weak self] proc in
            let unexpected = !(self?.manualStop ?? true) && proc.terminationReason == .exit
            DispatchQueue.main.async { self?.onExit?(unexpected) }
        }
        do {
            try p.run()
            process = p
            shellLog("start: gateway pid=\(p.processIdentifier) state=\(stateDir().path)")
        } catch {
            shellLog("start: gateway failed \(error)")
            onExit?(false)
        }
    }

    func terminateGracefully() {
        guard let p = process, p.isRunning else { return }
        manualStop = true
        p.interrupt() // SIGTERM → gateway 优雅停机（关 WSS 会话，≤15s）
        let deadline = Date().addingTimeInterval(20)
        DispatchQueue.global().async { [weak self] in
            while Date() < deadline, p.isRunning { Thread.sleep(forTimeInterval: 0.2) }
            if p.isRunning {
                kill(p.processIdentifier, SIGKILL)
                p.waitUntilExit()
            }
            DispatchQueue.main.async {
                self?.onExit?(false)
            }
        }
    }

    // MARK: - 旧进程清理

    // cleanupStaleServices 启动前清理本机所有相关旧服务，避免新旧实例并存
    // 抢占设备/平台会话/TUN/端口导致测试结果不可信：
    //   1. 占用监听端口的旧 gateway（如手动以源码形态跑的实例）；
    //   2. 旧 easytier-core（含 root/TUN 模式实例：普通 kill 失败时经免密 sudo pkill）；
    //   3. 孤儿 iproxy（旧 gateway 退出后残留的 USB 隧道，新实例会自动重建）。
    private func cleanupStaleServices() {
        shellLog("cleanup: start")
        killStaleGateway(onPort: listenPort)
        killAll(named: "easytier-core", label: "easytier-core", rootFallbackSudoPkill: "easytier-core")
        killAll(named: "iproxy", label: "iproxy", rootFallbackSudoPkill: nil)
        shellLog("cleanup: done")
    }

    // killStaleGateway 只杀占用端口且可执行名确认为 gateway 的进程；
    // 其他占用者不动（记录日志，由端口冲突自然暴露）。
    private func killStaleGateway(onPort port: Int) {
        var pids = listeners(on: port)
        for pid in pids {
            guard let comm = runCommand("/bin/ps", ["-p", "\(pid)", "-o", "comm="])?
                .trimmingCharacters(in: .whitespacesAndNewlines), !comm.isEmpty else { continue }
            let name = (comm as NSString).lastPathComponent
            guard name == "gateway" else {
                shellLog("cleanup: port \(port) occupied by \(name) (pid \(pid)), leaving it alone")
                continue
            }
            shellLog("cleanup: SIGTERM stale gateway pid \(pid) on port \(port)")
            kill(pid, SIGTERM)
        }
        // 旧实例优雅停机（关 WSS 会话，最长约 15s）；超时未退则强杀
        waitPortFree(port: port, timeout: 16)
        pids = listeners(on: port)
        for pid in pids {
            if commandName(pid) == "gateway" {
                shellLog("cleanup: force killing stale gateway pid \(pid)")
                kill(pid, SIGKILL)
            }
        }
    }

    // killAll 按可执行名杀全部进程（不含自己）；rootFallbackSudoPkill 非空时，
    // 对杀不动的 root 实例用免密 sudo pkill -f 兜底（sudoers 已放行该模式）。
    private func killAll(named name: String, label: String, rootFallbackSudoPkill: String?) {
        let out = runCommand("/bin/ps", ["-axco", "pid,comm"]) ?? ""
        var victims: [pid_t] = []
        for line in out.split(separator: "\n").dropFirst() {
            let parts = line.split(separator: " ", maxSplits: 1).map(String.init)
            guard parts.count == 2, let pid = pid_t(parts[0]), parts[1] == name else { continue }
            victims.append(pid)
        }
        guard !victims.isEmpty else { return }
        shellLog("cleanup: SIGTERM \(label) pids \(victims)")
        victims.forEach { kill($0, SIGTERM) }
        Thread.sleep(forTimeInterval: 3) // 给优雅退出留时间，之后统一复查强杀
        let after = runCommand("/bin/ps", ["-axco", "pid,comm"]) ?? ""
        var remain: [pid_t] = []
        for line in after.split(separator: "\n").dropFirst() {
            let parts = line.split(separator: " ", maxSplits: 1).map(String.init)
            guard parts.count == 2, let pid = pid_t(parts[0]), parts[1] == name else { continue }
            remain.append(pid)
        }
        if !remain.isEmpty {
            shellLog("cleanup: force killing \(label) pids \(remain)")
            remain.forEach { kill($0, SIGKILL) }
            if let pat = rootFallbackSudoPkill {
                // root 实例（如 TUN 模式 easytier-core）普通用户杀不动：免密 sudo pkill（已授权模式）
                _ = runCommand("/usr/bin/sudo", ["-n", "/usr/bin/pkill", "-f", pat])
                shellLog("cleanup: sudo pkill -f \(pat) issued for root \(label) instances")
            }
        }
    }

    private func commandName(_ pid: pid_t) -> String? {
        guard let comm = runCommand("/bin/ps", ["-p", "\(pid)", "-o", "comm="])?
            .trimmingCharacters(in: .whitespacesAndNewlines), !comm.isEmpty else { return nil }
        return (comm as NSString).lastPathComponent
    }

    private func waitPortFree(port: Int, timeout: TimeInterval) {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline, !listeners(on: port).isEmpty {
            Thread.sleep(forTimeInterval: 0.3)
        }
    }

    private func listeners(on port: Int) -> [pid_t] {
        guard let out = runCommand("/usr/sbin/lsof", ["-tiTCP:\(port)", "-sTCP:LISTEN"]) else { return [] }
        return out.split(separator: "\n").compactMap { pid_t($0) }
    }

    private func runCommand(_ path: String, _ args: [String]) -> String? {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: path)
        p.arguments = args
        let pipe = Pipe()
        p.standardOutput = pipe
        p.standardError = FileHandle.nullDevice
        do {
            try p.run()
            p.waitUntilExit()
        } catch {
            return nil
        }
        return String(data: pipe.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8)
    }
}

// shellLog 壳自身文件日志（排障）：~/Library/Application Support/WDAFarmGateway/logs/shell.log
// NSLog 只进统一日志，交付环境直接看文件更直接。
func shellLog(_ msg: String) {
    let dir = stateDir("logs")
    let url = dir.appendingPathComponent("shell.log")
    try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    if !FileManager.default.fileExists(atPath: url.path) {
        FileManager.default.createFile(atPath: url.path, contents: nil)
    }
    let line = "\(ISO8601DateFormatter().string(from: Date())) \(msg)\n"
    if let h = FileHandle(forWritingAtPath: url.path) {
        h.seekToEndOfFile()
        h.write(line.data(using: .utf8)!)
        try? h.close()
    }
    NSLog(msg)
}
