package main

import (
	"fmt"
	"os"

	"github.com/danielpaulus/go-ios/ios"
)

const (
	domain = "com.apple.mobile.wireless_lockdown"
)

var wifiKeys = []string{"EnableWifiConnections", "EnableWifiDebugging"}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: wifi-lockdown <udid>")
		os.Exit(2)
	}
	udid := os.Args[1]
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
	for _, key := range wifiKeys {
		if err := ld.SetValueForDomain(key, domain, true); err != nil {
			fmt.Fprintf(os.Stderr, "set %s: %v\n", key, err)
			os.Exit(1)
		}
		after, err := ld.GetValueForDomain(key, domain)
		if err != nil {
			fmt.Fprintf(os.Stderr, "get %s: %v\n", key, err)
			os.Exit(1)
		}
		fmt.Printf("%s %s=%v\n", udid, key, after)
	}
	if buddy, err := ld.GetValueForDomain("WirelessBuddyID", domain); err == nil {
		fmt.Printf("%s WirelessBuddyID=%v\n", udid, buddy)
	}
}
