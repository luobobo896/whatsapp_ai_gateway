//go:build windows

// WDA Farm Gateway Windows 桌面壳：拉起同目录 gateway.exe 子进程（0.0.0.0:8300），
// WebView2 窗口加载 http://127.0.0.1:8300/（与网页版同一页面），系统托盘菜单对齐 macOS DMG 壳。
package main

import (
	_ "embed"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/energye/systray"
	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

//go:embed icon.ico
var trayIcon []byte

const (
	appName      = "WDA Farm Gateway"
	mutexName    = "Local\\WDAFarmGatewaySingleInstance"
	defaultPort  = 8300
	readyTimeout = 60 * time.Second
)

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	pShowWindow          = user32.NewProc("ShowWindow")
	pSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	pMessageBoxW         = user32.NewProc("MessageBoxW")
	pOpenClipboard       = user32.NewProc("OpenClipboard")
	pCloseClipboard      = user32.NewProc("CloseClipboard")
	pEmptyClipboard      = user32.NewProc("EmptyClipboard")
	pSetClipboardData    = user32.NewProc("SetClipboardData")
	pGlobalAlloc         = kernel32.NewProc("GlobalAlloc")
	pGlobalLock          = kernel32.NewProc("GlobalLock")
	pGlobalUnlock        = kernel32.NewProc("GlobalUnlock")
	pGlobalFree          = kernel32.NewProc("GlobalFree")

	shellLogFile *os.File
)

func main() {
	runtime.LockOSThread()

	release, ok := ensureSingleInstance()
	if !ok {
		log.Println("already running")
		os.Exit(0)
	}
	defer release()

	port := defaultPort
	if v := os.Getenv("GATEWAY_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			port = n
		}
	}

	state := stateDir()
	for _, sub := range []string{"data", "logs"} {
		_ = os.MkdirAll(filepath.Join(state, sub), 0o755)
	}
	setupFileLog(filepath.Join(state, "logs", "shell.log"))
	shellLog("start: state=%s port=%d", state, port)

	gw := newGatewayProcess(state, port)
	_ = gw.cleanupStale()
	if err := gw.start(); err != nil {
		shellLog("gateway start failed: %v", err)
		fatalDialog("无法启动 gateway.exe：\n" + err.Error() + "\n\n请确认 WDAFarmGateway.exe 与 gateway.exe 在同一目录。")
		os.Exit(1)
	}
	defer gw.terminateGracefully()

	app := &desktopApp{gw: gw, port: port, state: state}
	systray.Run(app.onTrayReady, app.onTrayExit)
}

type desktopApp struct {
	gw    *gatewayProcess
	port  int
	state string

	mu     sync.Mutex
	wv     webview2.WebView
	wvOpen bool
}

func (a *desktopApp) onTrayReady() {
	systray.SetIcon(trayIcon)
	systray.SetTitle(appName)
	systray.SetTooltip(appName)

	mOpen := systray.AddMenuItem("打开管理页", "打开主窗口")
	mCopy := systray.AddMenuItem("复制局域网访问地址", "")
	systray.AddSeparator()
	mRestart := systray.AddMenuItem("重启后台服务", "")
	mState := systray.AddMenuItem("打开数据目录", "")
	mLogs := systray.AddMenuItem("打开日志", "")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出", "")

	mOpen.Click(func() { a.openWindow() })
	mCopy.Click(func() {
		addr := lanIPv4()
		if addr == "" {
			addr = "127.0.0.1"
		}
		_ = setClipboard(fmt.Sprintf("http://%s:%d", addr, a.port))
	})
	mRestart.Click(func() { a.restartGateway() })
	mState.Click(func() { _ = exec.Command("explorer", a.state).Start() })
	mLogs.Click(func() { _ = exec.Command("explorer", filepath.Join(a.state, "logs")).Start() })
	mQuit.Click(func() { systray.Quit() })

	go func() {
		if err := a.waitReady(readyTimeout); err != nil {
			shellLog("ready wait: %v", err)
			if !a.gw.isRunning() {
				shellLog("ready wait: gateway exited, auto-restart once")
				_ = a.gw.cleanupStale()
				if err2 := a.gw.start(); err2 != nil {
					shellLog("auto-restart start failed: %v", err2)
					systray.SetTooltip("网关启动失败 — 见 logs/shell.log")
				} else if err2 := a.waitReady(readyTimeout); err2 != nil {
					shellLog("auto-restart ready: %v", err2)
					systray.SetTooltip("网关启动超时 — 见 logs/shell.log")
				}
			} else {
				systray.SetTooltip("网关启动超时 — 见 logs/shell.log")
			}
		}
		a.openWindow()
	}()
}

func (a *desktopApp) onTrayExit() {
	a.mu.Lock()
	wv := a.wv
	a.wv = nil
	a.wvOpen = false
	a.mu.Unlock()
	if wv != nil {
		wv.Dispatch(func() { wv.Destroy() })
	}
	a.gw.terminateGracefully()
}

func (a *desktopApp) waitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("http://127.0.0.1:%d/api/cloud", a.port)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		if !a.gw.isRunning() {
			return fmt.Errorf("gateway exited before ready")
		}
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		resp, err := client.Do(req)
		if err == nil {
			code := resp.StatusCode
			resp.Body.Close()
			if code == 200 || code == 401 {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", url)
}

func (a *desktopApp) openWindow() {
	a.mu.Lock()
	if a.wvOpen {
		wv := a.wv
		a.mu.Unlock()
		if wv != nil {
			wv.Dispatch(func() {
				hwnd := uintptr(wv.Window())
				_, _, _ = pShowWindow.Call(hwnd, 9) // SW_RESTORE
				_, _, _ = pSetForegroundWindow.Call(hwnd)
			})
		}
		return
	}
	a.wvOpen = true
	a.mu.Unlock()

	go func() {
		runtime.LockOSThread()
		defer func() {
			a.mu.Lock()
			a.wv = nil
			a.wvOpen = false
			a.mu.Unlock()
		}()

		dataPath := filepath.Join(a.state, "webview2")
		_ = os.MkdirAll(dataPath, 0o755)

		w := webview2.NewWithOptions(webview2.WebViewOptions{
			Debug:     false,
			AutoFocus: true,
			DataPath:  dataPath,
			WindowOptions: webview2.WindowOptions{
				Title:  appName,
				Width:  1280,
				Height: 860,
				Center: true,
			},
		})
		if w == nil {
			shellLog("webview2: New failed (runtime missing?)")
			fatalDialog("无法创建 WebView2 窗口。\n请安装 Microsoft Edge WebView2 Runtime：\nhttps://developer.microsoft.com/microsoft-edge/webview2/")
			a.mu.Lock()
			a.wvOpen = false
			a.mu.Unlock()
			return
		}
		a.mu.Lock()
		a.wv = w
		a.mu.Unlock()

		w.SetSize(1280, 860, webview2.HintNone)
		if !a.gw.isRunning() {
			shellLog("webview: gateway not running, attempting restart before navigate")
			_ = a.gw.cleanupStale()
			if err := a.gw.start(); err != nil {
				shellLog("webview: restart start failed: %v", err)
				w.SetHtml(loadingHTML("后台服务未运行，自动重启失败：" + err.Error() + "。请从托盘「重启后台服务」"))
			} else if err := a.waitReady(30 * time.Second); err != nil {
				shellLog("webview: restart ready failed: %v", err)
				w.SetHtml(loadingHTML("后台服务未运行，请从托盘「重启后台服务」"))
			} else {
				w.Navigate(fmt.Sprintf("http://127.0.0.1:%d/", a.port))
			}
		} else {
			w.Navigate(fmt.Sprintf("http://127.0.0.1:%d/", a.port))
		}
		shellLog("webview: open http://127.0.0.1:%d/", a.port)
		w.Run()
		shellLog("webview: closed")
	}()
}

func (a *desktopApp) restartGateway() {
	a.gw.terminateGracefully()
	_ = a.gw.cleanupStale()
	if err := a.gw.start(); err != nil {
		shellLog("restart failed: %v", err)
		return
	}
	if err := a.waitReady(readyTimeout); err != nil {
		shellLog("restart ready: %v", err)
		return
	}
	a.mu.Lock()
	wv := a.wv
	port := a.port
	a.mu.Unlock()
	if wv != nil {
		wv.Dispatch(func() {
			wv.Navigate(fmt.Sprintf("http://127.0.0.1:%d/", port))
		})
	} else {
		a.openWindow()
	}
}

func loadingHTML(msg string) string {
	return `<html><body style="font-family:Segoe UI,sans-serif;background:#111;color:#eee;display:flex;align-items:center;justify-content:center;height:100vh;margin:0">
<div style="text-align:center"><h2>WDA Farm Gateway</h2><p>` + msg + `</p></div></body></html>`
}

type gatewayProcess struct {
	state string
	port  int

	mu      sync.Mutex
	cmd     *exec.Cmd
	logF    *os.File
	manual  bool
	running bool
	done    chan struct{}
}

func newGatewayProcess(state string, port int) *gatewayProcess {
	return &gatewayProcess{state: state, port: port}
}

func (g *gatewayProcess) isRunning() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.running
}

func (g *gatewayProcess) start() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running {
		return nil
	}
	bin, err := findGatewayExe()
	if err != nil {
		return err
	}
	resDir := filepath.Dir(bin)
	staticDir := filepath.Join(resDir, "static")
	if _, err := os.Stat(filepath.Join(staticDir, "index.html")); err != nil {
		staticDir = filepath.Join(g.state, "static")
	}
	logPath := filepath.Join(g.state, "logs", "gateway.log")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}

	cmd := exec.Command(bin,
		"-state", g.state,
		"-static", staticDir,
		"-listen", fmt.Sprintf("0.0.0.0:%d", g.port),
	)
	cmd.Dir = resDir
	cmd.Stdout = f
	cmd.Stderr = f
	binDir := filepath.Join(resDir, "bin")
	cmd.Env = append(os.Environ(),
		"WDA_GATEWAY_RESOURCES="+resDir,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP,
	}
	if err := cmd.Start(); err != nil {
		_ = f.Close()
		return err
	}
	g.cmd = cmd
	g.logF = f
	g.manual = false
	g.running = true
	g.done = make(chan struct{})
	done := g.done
	shellLog("gateway: started pid=%d bin=%s", cmd.Process.Pid, bin)
	go func() {
		err := cmd.Wait()
		shellLog("gateway: exit err=%v", err)
		g.mu.Lock()
		g.running = false
		if g.logF != nil {
			_ = g.logF.Close()
			g.logF = nil
		}
		close(done)
		g.mu.Unlock()
	}()
	return nil
}

func (g *gatewayProcess) terminateGracefully() {
	g.mu.Lock()
	if !g.running || g.cmd == nil || g.cmd.Process == nil {
		g.mu.Unlock()
		return
	}
	g.manual = true
	pid := g.cmd.Process.Pid
	done := g.done
	g.mu.Unlock()

	_ = windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(pid))
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		g.mu.Lock()
		if g.cmd != nil && g.cmd.Process != nil && g.running {
			_ = g.cmd.Process.Kill()
		}
		g.mu.Unlock()
		<-done
	}
}

// cleanupStale frees listen port before start: kill holders of the port and any
// leftover gateway.exe from the same install directory, then wait until Listen works.
func (g *gatewayProcess) cleanupStale() error {
	g.killPortHolders()
	g.killStaleGatewayExe()

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(g.port))
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			_ = ln.Close()
			shellLog("cleanup: port %d free", g.port)
			return nil
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	shellLog("cleanup: port %d still busy (%v)", g.port, lastErr)
	return lastErr
}

func (g *gatewayProcess) ownPID() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cmd != nil && g.cmd.Process != nil {
		return g.cmd.Process.Pid
	}
	return 0
}

func (g *gatewayProcess) killPortHolders() {
	out, err := exec.Command("netstat", "-ano").CombinedOutput()
	if err != nil {
		shellLog("cleanup: netstat failed: %v", err)
		return
	}
	portSuffix := fmt.Sprintf(":%d", g.port)
	own := g.ownPID()
	self := os.Getpid()
	seen := map[int]struct{}{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(strings.ToUpper(line), "LISTEN") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		local := fields[1]
		if !strings.HasSuffix(local, portSuffix) {
			// also match [::]:8300 / 0.0.0.0:8300 already covered by suffix
			continue
		}
		pid, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil || pid <= 0 {
			continue
		}
		if pid == own || pid == self {
			continue
		}
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		shellLog("cleanup: killing pid=%d holding port %d", pid, g.port)
		_ = exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid)).Run()
	}
}

func (g *gatewayProcess) killStaleGatewayExe() {
	bin, err := findGatewayExe()
	if err != nil {
		return
	}
	want, err := filepath.Abs(bin)
	if err != nil {
		want = bin
	}
	want = strings.ToLower(filepath.Clean(want))
	own := g.ownPID()
	self := os.Getpid()

	out, err := exec.Command("wmic", "process", "where", "name='gateway.exe'", "get", "ProcessId,ExecutablePath", "/FORMAT:CSV").CombinedOutput()
	if err != nil {
		// Fallback: taskkill by image name is too broad; try powershell CIM
		ps := fmt.Sprintf(
			`$want=%s; Get-CimInstance Win32_Process -Filter "Name='gateway.exe'" | ForEach-Object { if ($_.ExecutablePath -and ([IO.Path]::GetFullPath($_.ExecutablePath).ToLower() -eq $want)) { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue; Write-Output ("killed "+$_.ProcessId) } }`,
			strconv.Quote(want),
		)
		o2, err2 := exec.Command("powershell.exe", "-NoProfile", "-Command", ps).CombinedOutput()
		if err2 != nil {
			shellLog("cleanup: stale gateway lookup failed: %v / %v", err, err2)
			return
		}
		if t := strings.TrimSpace(string(o2)); t != "" {
			shellLog("cleanup: %s", t)
		}
		return
	}
	// CSV: Node,ExecutablePath,ProcessId
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(strings.ToLower(line), "node,") {
			continue
		}
		parts := parseCSVLine(line)
		if len(parts) < 3 {
			continue
		}
		path := strings.TrimSpace(parts[len(parts)-2])
		pidStr := strings.TrimSpace(parts[len(parts)-1])
		pid, err := strconv.Atoi(pidStr)
		if err != nil || pid <= 0 || pid == own || pid == self {
			continue
		}
		if path == "" {
			continue
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			abs = path
		}
		if strings.ToLower(filepath.Clean(abs)) != want {
			continue
		}
		shellLog("cleanup: killing stale gateway.exe pid=%d path=%s", pid, path)
		_ = exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid)).Run()
	}
}

func parseCSVLine(line string) []string {
	var fields []string
	var cur strings.Builder
	inQuotes := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '"':
			inQuotes = !inQuotes
		case c == ',' && !inQuotes:
			fields = append(fields, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	fields = append(fields, cur.String())
	return fields
}

func findGatewayExe() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(self)
	candidates := []string{
		filepath.Join(dir, "gateway.exe"),
		filepath.Join(dir, "gateway"),
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "gateway.exe"))
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	return "", fmt.Errorf("gateway.exe not found next to %s", self)
}

func stateDir() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base = "."
	}
	dir := filepath.Join(base, "WDAFarmGateway")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

func ensureSingleInstance() (func(), bool) {
	name, err := windows.UTF16PtrFromString(mutexName)
	if err != nil {
		return func() {}, true
	}
	h, err := windows.CreateMutex(nil, true, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		if h != 0 {
			_ = windows.CloseHandle(h)
		}
		return func() {}, false
	}
	if err != nil {
		return func() {}, true
	}
	return func() { _ = windows.CloseHandle(h) }, true
}

func setupFileLog(path string) {
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	shellLogFile = f
	log.SetOutput(io.MultiWriter(f, os.Stderr))  // file first: windowsgui stderr may error and MultiWriter would skip later writers
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
}

func shellLog(format string, args ...any) {
	log.Printf("[shell] "+format, args...)
	if shellLogFile != nil {
		_ = shellLogFile.Sync()
	}
}

func fatalDialog(msg string) {
	t, _ := windows.UTF16PtrFromString(appName)
	m, _ := windows.UTF16PtrFromString(msg)
	_, _, _ = pMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x10)
}

func lanIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			if v4 := ip.To4(); v4 != nil {
				return v4.String()
			}
		}
	}
	return ""
}

func setClipboard(text string) error {
	r, _, err := pOpenClipboard.Call(0)
	if r == 0 {
		return err
	}
	defer pCloseClipboard.Call()
	pEmptyClipboard.Call()
	u16, err2 := windows.UTF16FromString(text)
	if err2 != nil {
		return err2
	}
	size := len(u16) * 2
	const GMEM_MOVEABLE = 0x0002
	h, _, err := pGlobalAlloc.Call(GMEM_MOVEABLE, uintptr(size))
	if h == 0 {
		return err
	}
	ptr, _, err := pGlobalLock.Call(h)
	if ptr == 0 {
		pGlobalFree.Call(h)
		return err
	}
	mem := unsafe.Slice((*byte)(unsafe.Pointer(ptr)), size)
	src := unsafe.Slice((*byte)(unsafe.Pointer(&u16[0])), size)
	copy(mem, src)
	pGlobalUnlock.Call(h)
	const CF_UNICODETEXT = 13
	r, _, err = pSetClipboardData.Call(CF_UNICODETEXT, h)
	if r == 0 {
		pGlobalFree.Call(h)
		return err
	}
	return nil
}
