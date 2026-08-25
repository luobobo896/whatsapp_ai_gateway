package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danielpaulus/go-ios/ios"
	"howett.net/plist"
)

const (
	domain            = "com.apple.mobile.wireless_lockdown"
	needPasscodeToken = "NEED_DEVICE_PASSCODE"
	passcodeRetry     = 2 * time.Second
)

var wifiKeys = []string{"EnableWifiConnections", "EnableWifiDebugging"}

func itunesWirelessBuddyID() string {
	appdata := os.Getenv("APPDATA")
	if appdata == "" {
		return ""
	}
	p := filepath.Join(appdata, "Apple Computer", "Preferences", "com.apple.iTunes.plist")
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	var m map[string]any
	if _, err := plist.Unmarshal(b, &m); err != nil {
		return ""
	}
	if v, ok := m["WirelessBuddyID"].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func needsWirelessRebind(buddy, hostID string) bool {
	h := strings.TrimSpace(strings.ToUpper(hostID))
	if h == "" {
		return true
	}
	return strings.TrimSpace(strings.ToUpper(buddy)) != h
}

func buddyString(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func main() {
	wait := flag.Duration("wait", 60*time.Second, "wait for the iPhone lock-screen passcode prompt")
	statusOnly := flag.Bool("status", false, "print EnableWifiConnections/EnableWifiDebugging without writing")
	flag.Parse()
	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: wifi-lockdown [-wait 60s] [-status] <udid> [WirelessBuddyID]")
		os.Exit(2)
	}
	udid := flag.Arg(0)
	forcedBuddy := ""
	if flag.NArg() >= 2 {
		forcedBuddy = strings.TrimSpace(flag.Arg(1))
	}
	dev, err := ios.GetDevice(udid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "get device: %v\n", err)
		os.Exit(1)
	}
	ld, err := ios.ConnectLockdownWithSession(dev)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lockdown: %v\n", err)
		os.Exit(1)
	}
	defer ld.Close()
	if *statusOnly {
		for _, key := range wifiKeys {
			v, err := ld.GetValueForDomain(key, domain)
			if err != nil {
				fmt.Fprintf(os.Stderr, "get %s: %v\n", key, err)
				os.Exit(1)
			}
			fmt.Printf("%s %s=%v\n", udid, key, v)
		}
		return
	}

	hostID := forcedBuddy
	if hostID == "" {
		hostID = itunesWirelessBuddyID()
	}
	if hostID == "" {
		if rec, err := ios.ReadPairRecord(udid); err == nil {
			hostID = strings.TrimSpace(rec.HostID)
		}
	}
	buddy := ""
	if v, err := ld.GetValueForDomain("WirelessBuddyID", domain); err == nil {
		buddy = buddyString(v)
	}
	fmt.Printf("%s HostID=%s WirelessBuddyID=%s rebind=%v\n", udid, hostID, buddy, needsWirelessRebind(buddy, hostID))

	if needsWirelessRebind(buddy, hostID) {
		// 先关掉再打开，并把 Buddy 写成这台主机的 HostID（勾选 iTunes Wi-Fi 同步不会改 Buddy）。
		if err := ld.SetValueForDomain("EnableWifiConnections", domain, false); err != nil {
			fmt.Fprintf(os.Stderr, "clear EnableWifiConnections: %v\n", err)
			os.Exit(1)
		}
		if hostID != "" {
			if err := ld.SetValueForDomain("WirelessBuddyID", domain, hostID); err != nil {
				fmt.Fprintf(os.Stderr, "set WirelessBuddyID: %v\n", err)
				os.Exit(1)
			}
		}
	} else if err := ld.SetValueForDomain("EnableWifiConnections", domain, false); err != nil {
		// Buddy 已对齐时也要 bounce 一次，否则 _apple-mobdev2 可能仍宣告已关闭的旧端口。
		fmt.Fprintf(os.Stderr, "bounce EnableWifiConnections: %v\n", err)
		os.Exit(1)
	}

	prompted := false
	onPasscode := func() {
		if prompted {
			return
		}
		prompted = true
		fmt.Fprintln(os.Stderr, needPasscodeToken)
		fmt.Fprintln(os.Stderr, "请在 iPhone 上输入锁屏密码以开启无线调试（密码只在手机上输入）")
	}

	for _, key := range []string{"EnableWifiDebugging", "EnableWifiConnections"} {
		if err := ensureWifiKey(ld, key, *wait, onPasscode); err != nil {
			fmt.Fprintf(os.Stderr, "set %s: %v\n", key, err)
			if isPasscodeRequiredErr(err) || strings.Contains(err.Error(), needPasscodeToken) {
				os.Exit(3)
			}
			os.Exit(1)
		}
	}
	for _, key := range []string{"EnableWifiConnections", "EnableWifiDebugging", "WirelessBuddyID"} {
		after, err := ld.GetValueForDomain(key, domain)
		if err != nil {
			fmt.Printf("%s %s=<get %v>\n", udid, key, err)
			continue
		}
		fmt.Printf("%s %s=%v\n", udid, key, after)
	}
}

func ensureWifiKey(ld *ios.LockDownConnection, key string, wait time.Duration, onPasscode func()) error {
	if v, err := ld.GetValueForDomain(key, domain); err == nil && boolTrue(v) {
		return nil
	}
	if wait < 0 {
		wait = 0
	}
	deadline := time.Now().Add(wait)
	for {
		err := ld.SetValueForDomain(key, domain, true)
		if err == nil {
			return nil
		}
		if !isPasscodeRequiredErr(err) {
			return err
		}
		if onPasscode != nil {
			onPasscode()
		}
		if !time.Now().Before(deadline) {
			return passcodeTimeoutErr()
		}
		time.Sleep(passcodeRetry)
	}
}

func isPasscodeRequiredErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "PasscodeRequired") ||
		strings.Contains(s, "PasswordProtected") ||
		strings.Contains(s, "0xe80000ee") ||
		strings.Contains(s, "e80000ee")
}

func boolTrue(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(x, "true") || x == "1"
	default:
		return false
	}
}

func passcodeTimeoutErr() error {
	return fmt.Errorf("%s: 请在 iPhone 上输入锁屏密码以开启无线调试。若尚未设置锁屏密码，先到「设置 → 面容 ID 与密码 / 触控 ID 与密码」设置。密码只在手机上输入，不要发给电脑", needPasscodeToken)
}
