package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"wda-farm-gateway/internal/wda"
)

var udidRe = regexp.MustCompile(`([0-9A-Fa-f]{40})`)

// USBUDIDs 返回 USB 直连真机的 UDID（ioreg UsbAppleDeviceUDID）。
func USBUDIDs() []string {
	out, err := exec.Command("ioreg", "-p", "IOUSB", "-l", "-w0").Output()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var res []string
	for _, m := range regexp.MustCompile(`"UsbAppleDeviceUDID"\s*=\s*"([0-9A-Fa-f]{40})"`).FindAllStringSubmatch(string(out), -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			res = append(res, m[1])
		}
	}
	return res
}

// validSerial 校验 ideviceinfo 返回内容像硬件序列号（C38SG3S0HG00 / 新式 24 位），
// 排除 "ERROR: ..." 之类的错误文本。
func validSerial(s string) bool {
	if len(s) < 8 || len(s) > 24 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
			return false
		}
	}
	return true
}

// ideviceSerial 经 libimobiledevice 的 ideviceinfo 查询硬件序列号。
// 序列号只有 lockdownd 协议提供（ioreg/devicectl/WDA 都只有 UDID），需要设备 USB 在线且已配对。
// 二进制定位与 iproxy 同策略：PATH（壳注入 bundle bin）→ bundle 内置 → Homebrew。
func ideviceSerial(udid string) string {
	bin := "ideviceinfo"
	if _, err := exec.LookPath("ideviceinfo"); err != nil {
		bin = ""
		candidates := []string{}
		if res := os.Getenv("WDA_GATEWAY_RESOURCES"); res != "" {
			candidates = append(candidates, filepath.Join(res, "bin", "ideviceinfo"))
		}
		candidates = append(candidates, "/opt/homebrew/bin/ideviceinfo", "/usr/local/bin/ideviceinfo")
		for _, p := range candidates {
			if _, err := os.Stat(p); err == nil {
				bin = p
				break
			}
		}
		if bin == "" {
			return ""
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-u", udid, "-k", "SerialNumber")
	cmd.Env = append(os.Environ(), bundleLibFallback()...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(out))
	if !validSerial(s) {
		return ""
	}
	return s
}

// DiscoveredDevice 是发现到的设备（USB/CoreDevice）。
type DiscoveredDevice struct {
	UDID  string
	Name  string
	Model string
}

// Discover 合并 devicectl + ioreg，返回发现到的设备。
func Discover() []DiscoveredDevice {
	seen := map[string]bool{}
	var res []DiscoveredDevice
	for _, d := range devicectlDevices() {
		seen[d.UDID] = true
		res = append(res, d)
	}
	for _, u := range USBUDIDs() {
		if !seen[u] {
			res = append(res, DiscoveredDevice{UDID: u})
		}
	}
	return res
}

func devicectlDevices() []DiscoveredDevice {
	out, err := exec.Command("xcrun", "devicectl", "list", "devices").Output()
	if err != nil {
		return nil
	}
	var res []DiscoveredDevice
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 4 {
			continue
		}
		ident := parts[len(parts)-3]
		state := parts[len(parts)-2]
		if state != "available" || !udidRe.MatchString(ident) {
			continue
		}
		model := parts[len(parts)-1]
		name := strings.Join(parts[:len(parts)-4], " ")
		res = append(res, DiscoveredDevice{UDID: ident, Name: name, Model: model})
	}
	return res
}

// privateSubnets 返回本机所有私网 IPv4 /24 网段（10/8、172.16/12、192.168/16）。
func privateSubnets() []string { return subnetsOf(nil) }

// physicalSubnets 返回物理网卡（macOS en*，含 USB 网卡）所在私网 /24。
// 手机与网关通常同接一个物理网络，优先扫这里可把每轮扫描从全网段数秒降到 ~1s；
// VPN/代理 TUN、虚拟机桥等虚拟网卡（utun/bridge/awdl/anpi/vmenet）只在兜底全网段扫描时参与。
// 无 en* 网卡的环境（Linux 等）回退全部私网网段。
func physicalSubnets() []string {
	return subnetsOf(func(name string) bool { return strings.HasPrefix(name, "en") })
}

// subnetsOf 收集满足 name 过滤条件的接口（nil=不过滤）上的私网 /24 网段。
func subnetsOf(keep func(name string) bool) []string {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	set := map[string]bool{}
	for _, ifc := range ifs {
		if keep != nil && !keep(ifc.Name) {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP.To4()
			if ip == nil || !isPrivateIPv4(ip) || ip.IsLoopback() {
				continue
			}
			set[fmt.Sprintf("%d.%d.%d.0/24", ip[0], ip[1], ip[2])] = true
		}
	}
	var subs []string
	for s := range set {
		subs = append(subs, s)
	}
	sort.Strings(subs)
	return subs
}

func isPrivateIPv4(ip net.IP) bool {
	a, b := ip[0], ip[1]
	if a == 10 {
		return true
	}
	if a == 172 && b >= 16 && b <= 31 {
		return true
	}
	if a == 192 && b == 168 {
		return true
	}
	return false
}

// FoundWDA 是局域网扫描到的 WDA 实例。
type FoundWDA struct {
	IP         string
	IOSIP      string
	IOSVersion string
	UUID       string
	Name       string
	Model      string
}

// ScanLANWDA 并发扫描本机全部私网 /24 的 8100 端口，返回就绪的 WDA。
func ScanLANWDA(timeout time.Duration) []FoundWDA {
	return scanSubnets(privateSubnets(), timeout)
}

// scanSubnets 并发扫描指定网段列表的 8100 端口（物理网卡快路径与全网段共用）。
func scanSubnets(subs []string, timeout time.Duration) []FoundWDA {
	httpClient := &http.Client{Timeout: timeout}
	var mu sync.Mutex
	var res []FoundWDA
	var wg sync.WaitGroup
	sem := make(chan struct{}, 64)
	for _, sub := range subs {
		_, ipnet, err := net.ParseCIDR(sub)
		if err != nil {
			continue
		}
		for ip := ipnet.IP.Mask(ipnet.Mask); ipnet.Contains(ip); incIP(ip) {
			host := ip.String()
			wg.Add(1)
			go func(h string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if f, ok := probeWDA(h, httpClient); ok {
					mu.Lock()
					res = append(res, f)
					mu.Unlock()
				}
			}(host)
		}
	}
	wg.Wait()
	return res
}

func incIP(ip net.IP) {
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			return
		}
	}
}

func probeWDA(host string, client *http.Client) (FoundWDA, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), client.Timeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+host+":8100/status", nil)
	resp, err := client.Do(req)
	if err != nil {
		return FoundWDA{}, false
	}
	defer resp.Body.Close()
	var envelope struct {
		Value struct {
			Ready bool `json:"ready"`
			OS    struct {
				Version string `json:"version"`
			} `json:"os"`
			IOS struct {
				IP string `json:"ip"`
			} `json:"ios"`
		} `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil || !envelope.Value.Ready {
		return FoundWDA{}, false
	}
	info := wdaDeviceInfo(host)
	return FoundWDA{
		IP: host, IOSIP: envelope.Value.IOS.IP, IOSVersion: envelope.Value.OS.Version,
		UUID: info.UUID, Name: info.Name, Model: info.Model,
	}, true
}

// wdaDeviceInfo 读取某 WDA 的 /wda/device/info（identifierForVendor uuid/name/model）。
func wdaDeviceInfo(host string) struct{ UUID, Name, Model string } {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + host + ":8100/wda/device/info")
	if err != nil {
		return struct{ UUID, Name, Model string }{}
	}
	defer resp.Body.Close()
	var envelope struct {
		Value struct {
			UUID  string `json:"uuid"`
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"value"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&envelope)
	return struct{ UUID, Name, Model string }{envelope.Value.UUID, envelope.Value.Name, envelope.Value.Model}
}

// iosMajor 解析 iOS 主版本号（如 "15.8.8" -> 15）。
func iosMajor(v string) int {
	parts := strings.Split(v, ".")
	if len(parts) == 0 {
		return 0
	}
	n, _ := strconv.Atoi(parts[0])
	return n
}

// WDAHealth 是某设备 WDA 健康快照。
type WDAHealth struct {
	OK      bool   `json:"ok"`
	Ready   bool   `json:"ready"`
	IP      string `json:"ip"`
	Version string `json:"ios_version"`
	Error   string `json:"error,omitempty"`
}

// CheckWDA 探测某 IP:port 的 WDA /status。
func CheckWDA(ip string, port int, timeout time.Duration) WDAHealth {
	if port == 0 {
		port = 8100
	}
	c := wda.NewClient(fmt.Sprintf("http://%s:%d", ip, port), timeout)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	st, err := c.Status(ctx)
	if err != nil {
		return WDAHealth{OK: false, Error: err.Error()}
	}
	return WDAHealth{OK: st.Ready, Ready: st.Ready, IP: st.IOSIP, Version: st.OSVersion}
}
