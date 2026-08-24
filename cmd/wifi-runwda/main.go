// wifi-runwda 让 go-ios 走 usbmux 的 Network 设备（Apple 无线调试通道）。
// 有 Network 条目时 testmanagerd DTX 不走 USB，拔线后 XCTest / :8100 可以继续。
// Mac 用 unix usbmux，Windows 用 127.0.0.1:27015，同一套逻辑。
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"howett.net/plist"
)

func main() {
	udid := flag.String("udid", "", "device udid")
	ip := flag.String("ip", "", "device wifi ipv4 (optional, for :62078 wait)")
	bundle := flag.String("bundle", "com.wda.WebRunner.xctrunner", "wda runner bundle id")
	port := flag.Int("port", 8100, "USE_PORT")
	iosBin := flag.String("ios", "ios", "go-ios binary")
	serveOnly := flag.Bool("serve", false, "only run usbmux proxy")
	waitNet := flag.Duration("wait-network", 10*time.Second, "wait up to this long for usbmux ConnectionType=Network DeviceID; fall back to USB if still missing (USB unplug would stop Automation Running)")
	flag.Parse()
	if *udid == "" {
		fmt.Fprintln(os.Stderr, "usage: wifi-runwda -udid <udid> [-ip wifi-ip] [-bundle id] [-port 8100] [-ios ios]")
		os.Exit(2)
	}
	if *ip != "" {
		_ = waitTCP(*ip, 62078, 3*time.Second)
	}

	if *waitNet > 0 {
		log.Printf("waiting up to %s for usbmux ConnectionType=Network on %s", *waitNet, short(*udid))
	}
	dev, ok := waitPreferNetwork(*udid, *waitNet, nil)
	route := chooseNetworkRoute(dev, ok)
	if route.Via == "" {
		fmt.Fprintf(os.Stderr, "usbmux 没有设备 %s\n", short(*udid))
		os.Exit(1)
	}
	if route.Via != "usbmux-network" {
		if err := requireMuxNetwork(*udid, route.Target, true); err != nil {
			// 拿不到 usbmux Network（无线调试配对未建立）。回退 USB 激活，
			// 让设备能先跑起来（Automation Running / 可发消息）；系统按
			// usbmuxNetworkUDIDs() 判 `unplugSafeFor=false`，UI 会标记为非拔线保活。
			log.Printf("WARN %s; 无线调试 Network 条目缺失，回退 USB 激活（拔 USB 会拆掉 Automation Running）", err)
		}
	}
	log.Printf("using ConnectionType=%s via=%s usbmux_id=%d udid=%s",
		route.Target.Type, route.Via, route.Target.ID, short(*udid))

	ln, muxAddr, err := listenMux(*udid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen mux: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = ln.Close(); cleanupMux(muxAddr) }()

	st := &muxState{udid: *udid, target: route.Target}
	go serveMux(ln, st)
	if *serveOnly {
		fmt.Println(muxAddr)
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		<-ch
		return
	}

	args := []string{
		"--udid=" + *udid,
		"runwda",
		"--bundleid=" + *bundle,
		"--testrunnerbundleid=" + *bundle,
		"--xctestconfig=WebDriverAgentRunner.xctest",
		"--env=USE_PORT=" + strconv.Itoa(*port),
		"--env=WDA_DEVICE_UDID=" + *udid,
	}
	// 注意：不要注入 USE_IP。隧道（USB/Network iproxy）都转发设备的 loopback:8100，
	// WDA 绑定 Wi-Fi IP 会让 loopback 不再监听、隧道全部失效；而 Wi-Fi 直连又依赖
	// iOS「本地网络」权限。保持 WDA 监听 loopback，靠 usbmux 隧道访问最稳。
	cmd := exec.Command(*iosBin, args...)
	cmd.Env = append(os.Environ(), "USBMUXD_SOCKET_ADDRESS="+muxAddr)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "start ios: %v\n", err)
		os.Exit(1)
	}
	log.Printf("wifi-runwda started pid=%d udid=%s mux=%s ConnectionType=%s via=%s",
		cmd.Process.Pid, short(*udid), muxAddr, route.Target.Type, route.Via)

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case sig := <-ch:
		_ = cmd.Process.Signal(sig)
		err = <-done
	case err = <-done:
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ios runwda: %v\n", err)
		os.Exit(1)
	}
}

func short(udid string) string {
	if len(udid) > 8 {
		return udid[:8]
	}
	return udid
}

func waitTCP(ip string, port int, d time.Duration) error {
	deadline := time.Now().Add(d)
	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	var last error
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = c.Close()
			return nil
		}
		last = err
		time.Sleep(200 * time.Millisecond)
	}
	return last
}

type muxHeader struct {
	Length  uint32
	Version uint32
	Request uint32
	Tag     uint32
}

type muxState struct {
	udid   string
	target muxDevice
}

func serveMux(ln net.Listener, st *muxState) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go handleMuxConn(c, st)
	}
}

func handleMuxConn(c net.Conn, st *muxState) {
	defer c.Close()
	for {
		h, payload, err := readMux(c)
		if err != nil {
			return
		}
		var req map[string]any
		if _, err := plist.Unmarshal(payload, &req); err != nil {
			return
		}
		switch req["MessageType"].(string) {
		case "ListDevices":
			_ = writeMux(c, h, map[string]any{"DeviceList": []any{attachedDevice(st.udid, st.target.Type)}})
		case "Listen":
			_ = writeMux(c, h, map[string]any{"MessageType": "Result", "Number": 0})
			_ = writeMux(c, muxHeader{Version: h.Version, Request: 8, Tag: 0}, attachedDevice(st.udid, st.target.Type))
		case "Connect":
			handleConnect(c, h, req, st)
			return
		default:
			forwardReal(c, h, payload)
		}
	}
}

func attachedDevice(udid, connType string) map[string]any {
	if connType == "" {
		connType = "USB"
	}
	return map[string]any{
		"DeviceID":    1,
		"MessageType": "Attached",
		"Properties": map[string]any{
			"ConnectionType": connType,
			"DeviceID":       1,
			"LocationID":     0,
			"ProductID":      0,
			"SerialNumber":   udid,
		},
	}
}

func listenMux(udid string) (net.Listener, string, error) {
	if runtime.GOOS == "windows" {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, "", err
		}
		return ln, ln.Addr().String(), nil
	}
	sock := filepath.Join(os.TempDir(), "wda-wifi-mux-"+short(udid)+".sock")
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, "", err
	}
	return ln, "unix://" + sock, nil
}

func cleanupMux(muxAddr string) {
	if runtime.GOOS == "windows" {
		return
	}
	if p, ok := strings.CutPrefix(muxAddr, "unix://"); ok {
		_ = os.Remove(p)
	}
}

func handleConnect(c net.Conn, h muxHeader, req map[string]any, st *muxState) {
	// usbmux Connect has no ConnectionType field. Selecting the Network
	// DeviceID is how remotexpc / Appium specify wireless.
	req["DeviceID"] = uint32(st.target.ID)
	log.Printf("connect ConnectionType=%s muxport=%d -> usbmux DeviceID=%d",
		st.target.Type, plistUint16(req["PortNumber"]), st.target.ID)
	forwardConnectUSB(c, h, req)
}

func forwardConnectUSB(c net.Conn, h muxHeader, req map[string]any) {
	real, err := realMuxDial()
	if err != nil {
		_ = writeMux(c, h, map[string]any{"MessageType": "Result", "Number": 2})
		return
	}
	payload, err := plist.Marshal(req, plist.XMLFormat)
	if err != nil {
		_ = real.Close()
		_ = writeMux(c, h, map[string]any{"MessageType": "Result", "Number": 2})
		return
	}
	h.Length = uint32(16 + len(payload))
	h.Request = 8
	if err := binary.Write(real, binary.LittleEndian, h); err != nil {
		_ = real.Close()
		return
	}
	if _, err := real.Write(payload); err != nil {
		_ = real.Close()
		return
	}
	rh, rpayload, err := readMux(real)
	if err != nil {
		_ = real.Close()
		return
	}
	rh.Tag = h.Tag
	if err := binary.Write(c, binary.LittleEndian, rh); err != nil {
		_ = real.Close()
		return
	}
	if _, err := c.Write(rpayload); err != nil {
		_ = real.Close()
		return
	}
	var result map[string]any
	_, _ = plist.Unmarshal(rpayload, &result)
	if plistUint16(result["Number"]) != 0 {
		_ = real.Close()
		return
	}
	splice(c, real)
}

func splice(a, b net.Conn) {
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(b, a)
		_ = b.Close()
		close(done)
	}()
	_, _ = io.Copy(a, b)
	_ = a.Close()
	<-done
}

func realMuxDial() (net.Conn, error) {
	if runtime.GOOS == "windows" {
		return net.DialTimeout("tcp", "127.0.0.1:27015", time.Second)
	}
	return net.DialTimeout("unix", "/var/run/usbmuxd", time.Second)
}

func listRealDevices() []muxDevice {
	real, err := realMuxDial()
	if err != nil {
		return nil
	}
	defer real.Close()
	req := map[string]any{
		"MessageType":         "ListDevices",
		"ClientVersionString": "go-usbmux-0.0.1",
		"ProgName":            "wifi-runwda",
	}
	h := muxHeader{Version: 1, Request: 8, Tag: 1}
	if err := writeMux(real, h, req); err != nil {
		return nil
	}
	_, payload, err := readMux(real)
	if err != nil {
		return nil
	}
	var resp struct {
		DeviceList []struct {
			DeviceID   int
			Properties struct {
				SerialNumber   string
				ConnectionType string
			}
		}
	}
	if _, err := plist.Unmarshal(payload, &resp); err != nil {
		return nil
	}
	out := make([]muxDevice, 0, len(resp.DeviceList))
	for _, d := range resp.DeviceList {
		out = append(out, muxDevice{
			ID:   d.DeviceID,
			UDID: d.Properties.SerialNumber,
			Type: d.Properties.ConnectionType,
		})
	}
	return out
}

func plistUint16(v any) uint16 {
	switch n := v.(type) {
	case uint64:
		return uint16(n)
	case int64:
		return uint16(n)
	case uint32:
		return uint16(n)
	case int:
		return uint16(n)
	case uint16:
		return n
	default:
		return 0
	}
}

func readMux(r io.Reader) (muxHeader, []byte, error) {
	var h muxHeader
	if err := binary.Read(r, binary.LittleEndian, &h); err != nil {
		return h, nil, err
	}
	if h.Length < 16 || h.Length > 1<<20 {
		return h, nil, io.ErrUnexpectedEOF
	}
	payload := make([]byte, h.Length-16)
	if _, err := io.ReadFull(r, payload); err != nil {
		return h, nil, err
	}
	return h, payload, nil
}

func writeMux(w io.Writer, h muxHeader, body any) error {
	payload, err := plist.Marshal(body, plist.XMLFormat)
	if err != nil {
		return err
	}
	h.Length = uint32(16 + len(payload))
	h.Request = 8
	if err := binary.Write(w, binary.LittleEndian, h); err != nil {
		return err
	}
	_, err = w.Write(payload)
	return err
}

func forwardReal(c net.Conn, h muxHeader, payload []byte) {
	real, err := realMuxDial()
	if err != nil {
		_ = writeMux(c, h, map[string]any{"MessageType": "Result", "Number": 2})
		return
	}
	defer real.Close()
	h.Length = uint32(16 + len(payload))
	if err := binary.Write(real, binary.LittleEndian, h); err != nil {
		return
	}
	if _, err := real.Write(payload); err != nil {
		return
	}
	rh, rpayload, err := readMux(real)
	if err != nil {
		return
	}
	rh.Tag = h.Tag
	if err := binary.Write(c, binary.LittleEndian, rh); err != nil {
		return
	}
	_, _ = c.Write(rpayload)
}
