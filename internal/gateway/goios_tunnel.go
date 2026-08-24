package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// go-ios 1.3.2 默认把隧道信息 HTTP API 绑在 127.0.0.1:28100。
// 启动与探测都写死这个端口，避免本机残留 agent 占用随机端口后 runwda 对不上。
const (
	goiosTunnelInfoHost = "127.0.0.1"
	goiosTunnelInfoPort = 28100
)

type goiosTunnelInfo struct {
	Address          string `json:"address"`
	RsdPort          int    `json:"rsdPort"`
	Udid             string `json:"udid"`
	UserspaceTUN     bool   `json:"userspaceTun"`
	UserspaceTUNPort int    `json:"userspaceTunPort"`
}

type goiosTunnelDaemon struct {
	mu          sync.Mutex
	cmd         *exec.Cmd
	done        chan struct{}
	desired     bool
	adopted     bool
	userspace   bool
	lastErr     string
	lastRestart time.Time
}

var goiosTunnel = &goiosTunnelDaemon{}

func goiosTunnelStartArgs(userspace bool) []string {
	args := []string{"tunnel", "start", fmt.Sprintf("--tunnel-info-port=%d", goiosTunnelInfoPort)}
	if userspace {
		args = append(args, "--userspace")
	}
	return args
}

func withGoIOSTunnelPort(args []string) []string {
	return append([]string{fmt.Sprintf("--tunnel-info-port=%d", goiosTunnelInfoPort)}, args...)
}

func (d *goiosTunnelDaemon) Running() bool {
	d.mu.Lock()
	cmd := d.cmd
	done := d.done
	d.mu.Unlock()
	if tunnelAgentReady() {
		return true
	}
	if cmd == nil || done == nil {
		return false
	}
	select {
	case <-done:
		return false
	default:
		return true
	}
}

func (d *goiosTunnelDaemon) Status() map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	return map[string]any{
		"running":    d.cmd != nil || d.adopted,
		"desired":    d.desired,
		"adopted":    d.adopted,
		"userspace":  d.userspace,
		"ready":      tunnelAgentReady(),
		"last_error": d.lastErr,
		"info_url":   fmt.Sprintf("http://%s:%d", goiosTunnelInfoHost, goiosTunnelInfoPort),
	}
}

// Ensure 拉起用户态 ios tunnel start。已有健康 agent 则复用，不抢端口。
func (d *goiosTunnelDaemon) Ensure() error {
	d.mu.Lock()
	d.desired = true
	d.mu.Unlock()
	if tunnelAgentReady() {
		d.mu.Lock()
		if d.cmd == nil {
			d.adopted = true
			d.lastErr = ""
		}
		d.mu.Unlock()
		slog.Info("go-ios tunnel agent already running", "port", goiosTunnelInfoPort)
		return nil
	}
	d.mu.Lock()
	if d.cmd != nil && d.done != nil {
		select {
		case <-d.done:
		default:
			d.mu.Unlock()
			if waitTunnelAgent(20 * time.Second) {
				return nil
			}
			return fmt.Errorf("go-ios 隧道进程在跑，但 :%d 尚未就绪", goiosTunnelInfoPort)
		}
	}
	d.mu.Unlock()

	bin := lookTool("ios", "ios.exe")
	if bin == "" {
		return fmt.Errorf("iOS 17+ 需要 go-ios 隧道，未找到 ios/ios.exe。请放到 PATH 或 WDA_GATEWAY_RESOURCES/bin")
	}
	args := goiosTunnelStartArgs(true)
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), bundleLibFallback()...)
	logPath := filepath.Join(os.TempDir(), "goios-tunnel.log")
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		cmd.Stdout, cmd.Stderr = f, f
	}
	if err := cmd.Start(); err != nil {
		d.mu.Lock()
		d.lastErr = err.Error()
		d.mu.Unlock()
		return fmt.Errorf("启动 ios tunnel start --userspace 失败：%w", err)
	}
	done := make(chan struct{})
	go func() {
		err := cmd.Wait()
		close(done)
		d.mu.Lock()
		if d.cmd == cmd {
			d.cmd = nil
			d.done = nil
			if err != nil {
				d.lastErr = err.Error()
			}
		}
		d.mu.Unlock()
		if err != nil {
			slog.Warn("go-ios tunnel process exited", "error", err, "log", logPath)
		}
	}()
	d.mu.Lock()
	d.cmd = cmd
	d.done = done
	d.userspace = true
	d.adopted = false
	d.lastRestart = time.Now()
	d.lastErr = ""
	d.mu.Unlock()
	slog.Info("go-ios tunnel started", "userspace", true, "port", goiosTunnelInfoPort, "log", logPath)
	if waitTunnelAgent(25 * time.Second) {
		return nil
	}
	d.mu.Lock()
	select {
	case <-done:
		errText := d.lastErr
		d.mu.Unlock()
		if errText == "" {
			errText = "进程已退出"
		}
		return fmt.Errorf("ios tunnel start --userspace 未能就绪（%s）。iOS 17.0–17.3 请升级到 17.4+；Windows 内核隧道需 wintun.dll。日志 %s", errText, logPath)
	default:
		d.mu.Unlock()
	}
	return fmt.Errorf("ios tunnel start 已启动，但 %d 端口 25s 内未就绪。见 %s", goiosTunnelInfoPort, logPath)
}

// WaitDevice 等到该 UDID 出现在隧道列表。仅 iOS 17+ 激活路径调用。
func (d *goiosTunnelDaemon) WaitDevice(udid string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		if !tunnelAgentReady() {
			last = "agent not ready"
			time.Sleep(500 * time.Millisecond)
			continue
		}
		infos, err := listGoIOSTunnels()
		if err != nil {
			last = err.Error()
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if tunnelHasDevice(infos, udid) {
			slog.Info("go-ios tunnel ready for device", "udid", shortOf(udid))
			return nil
		}
		last = fmt.Sprintf("tunnel list has %d device(s), not %s", len(infos), shortOf(udid))
		time.Sleep(700 * time.Millisecond)
	}
	return fmt.Errorf("iOS 17+ 隧道未建立（%s）。请确认 USB 已插、已点信任此电脑、开发者模式已开。日志见 %s", last, filepath.Join(os.TempDir(), "goios-tunnel.log"))
}

func (d *goiosTunnelDaemon) Supervise() {
	d.mu.Lock()
	desired := d.desired
	d.mu.Unlock()
	if !desired {
		return
	}
	if d.Running() {
		return
	}
	d.mu.Lock()
	if time.Since(d.lastRestart) < 2*time.Minute {
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()
	slog.Warn("go-ios tunnel missing, supervising restart")
	if err := d.Ensure(); err != nil {
		slog.Warn("go-ios tunnel supervise failed", "error", err)
	}
}

// StopGoIOSTunnel 网关退出时停掉本进程拉起的隧道守护；复用别人的 agent 不动。
func StopGoIOSTunnel() {
	goiosTunnel.Stop()
}

func (d *goiosTunnelDaemon) Stop() {
	d.mu.Lock()
	d.desired = false
	cmd := d.cmd
	done := d.done
	adopted := d.adopted
	d.mu.Unlock()
	if adopted && cmd == nil {
		return
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(os.Interrupt)
		if done != nil {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
			}
		}
	}
}

func tunnelAgentURL(path string) string {
	return fmt.Sprintf("http://%s:%d%s", goiosTunnelInfoHost, goiosTunnelInfoPort, path)
}

func tunnelAgentReady() bool {
	client := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := client.Get(tunnelAgentURL("/health"))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

func waitTunnelAgent(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if tunnelAgentReady() {
			client := &http.Client{Timeout: 800 * time.Millisecond}
			if resp, err := client.Get(tunnelAgentURL("/ready")); err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return true
				}
			}
			// /health 通而 /ready 未通：agent 已起来，继续等首轮 UpdateTunnels。
		}
		time.Sleep(300 * time.Millisecond)
	}
	return tunnelAgentReady()
}

func listGoIOSTunnels() ([]goiosTunnelInfo, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(tunnelAgentURL("/tunnels"))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tunnel ls http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return parseGoIOSTunnelList(string(body))
}

func parseGoIOSTunnelList(raw string) ([]goiosTunnelInfo, error) {
	arr := extractJSONArray(raw)
	if arr != "" {
		var infos []goiosTunnelInfo
		if err := json.Unmarshal([]byte(arr), &infos); err == nil {
			return infos, nil
		}
	}
	obj := extractJSONObject(raw)
	if obj != "" {
		var wrap struct {
			Tunnels    []goiosTunnelInfo `json:"tunnels"`
			TunnelList []goiosTunnelInfo `json:"tunnelList"`
		}
		if err := json.Unmarshal([]byte(obj), &wrap); err == nil {
			if len(wrap.Tunnels) > 0 {
				return wrap.Tunnels, nil
			}
			return wrap.TunnelList, nil
		}
	}
	return nil, fmt.Errorf("cannot parse tunnel list")
}

func tunnelHasDevice(infos []goiosTunnelInfo, udid string) bool {
	for _, t := range infos {
		if strings.EqualFold(t.Udid, udid) && (t.Address != "" || t.RsdPort > 0 || t.UserspaceTUNPort > 0) {
			return true
		}
	}
	return false
}

func liveGoIOSTunnelSet() map[string]bool {
	out := map[string]bool{}
	if !tunnelAgentReady() {
		return out
	}
	infos, err := listGoIOSTunnels()
	if err != nil {
		return out
	}
	for _, t := range infos {
		if t.Udid != "" {
			out[strings.ToUpper(t.Udid)] = true
		}
	}
	return out
}
