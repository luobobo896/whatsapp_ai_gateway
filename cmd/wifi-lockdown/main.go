package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/danielpaulus/go-ios/ios"
	"howett.net/plist"
)

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

const domain = "com.apple.mobile.wireless_lockdown"

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
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: wifi-lockdown <udid>")
		os.Exit(2)
	}
	udid := os.Args[1]
	forcedBuddy := ""
	if len(os.Args) >= 3 {
		forcedBuddy = strings.TrimSpace(os.Args[2])
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
	}
	for _, key := range []string{"EnableWifiDebugging", "EnableWifiConnections"} {
		if err := ld.SetValueForDomain(key, domain, true); err != nil {
			fmt.Fprintf(os.Stderr, "set %s: %v\n", key, err)
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