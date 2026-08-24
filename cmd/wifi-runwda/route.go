package main

import (
	"fmt"
	"time"
)

// muxLockdownPort is go-ios Lockdownport (htons 62078 on little-endian).
const muxLockdownPortA uint16 = 32498
const muxLockdownPortB uint16 = 32500

type muxDevice struct {
	ID   int
	UDID string
	Type string
}

// connectRoute is how we honor usbmux ConnectionType=Network.
// remotexpc Connect has no ConnectionType field: listDevices(), then
// Connect the DeviceID whose Properties.ConnectionType is Network.
type connectRoute struct {
	Target muxDevice
	Via    string // usbmux-network | usbmux-usb
}

// pickDevice prefers usbmux Network so testmanagerd DTX rides Wi-Fi.
// USB is only used when the host has not attached a network device yet.
func pickDevice(devs []muxDevice, udid string) (muxDevice, bool) {
	var usb muxDevice
	var foundUSB bool
	for _, d := range devs {
		if d.UDID != udid {
			continue
		}
		if d.Type == "Network" {
			return d, true
		}
		usb = d
		foundUSB = true
	}
	if foundUSB {
		return usb, true
	}
	return muxDevice{}, false
}

// chooseNetworkRoute explicitly selects ConnectionType=Network.
// usbmuxd ListDevices is the only place that field exists; Connect only
// takes DeviceID+PortNumber. Raw TCP to :62078 can talk lockdown but
// testmanagerd/installation_proxy ports RST unless usbmuxd itself attached
// the Network DeviceID (see appium-ios-remotexpc README).
func chooseNetworkRoute(dev muxDevice, found bool) connectRoute {
	if found && dev.Type == "Network" && dev.ID > 0 {
		return connectRoute{Target: dev, Via: "usbmux-network"}
	}
	if found {
		return connectRoute{Target: dev, Via: "usbmux-usb"}
	}
	return connectRoute{}
}

func htons(p uint16) uint16 {
	return p<<8 | p>>8
}

func muxPortToTCP(p uint16) uint16 {
	if p == muxLockdownPortA || p == muxLockdownPortB {
		return 62078
	}
	swapped := htons(p)
	if swapped == 62078 || swapped >= 1024 {
		return swapped
	}
	return p
}

// waitPreferNetwork polls usbmux until a Network entry appears (or timeout).
// EnableWifiConnections 写入后，无线设备常要数秒到数十秒才挂上；立刻选 USB
// 会让 testmanagerd 仍绑死线缆，拔线必拆 XCTest。
// requireMuxNetwork 拒绝 USB 回退：没有 Network 条目时 XCTest 绑死线缆，拔线必拆 Automation Running。
func requireMuxNetwork(udid string, dev muxDevice, found bool) error {
	if !found {
		return fmt.Errorf("usbmux 没有设备 %s", short(udid))
	}
	if dev.Type != "Network" {
		have := dev.Type
		if have == "" {
			have = "unknown"
		}
		return fmt.Errorf("usbmux 没有 Network 条目（%s 当前是 %s）。请打开 Finder/Xcode「在无线局域网上显示此 iPhone」，等 ios list 出现 Network 后再激活；USB 激活拔线会拆掉 Automation Running", short(udid), have)
	}
	return nil
}

func waitPreferNetwork(udid string, timeout time.Duration, list func() []muxDevice) (muxDevice, bool) {
	if list == nil {
		list = listRealDevices
	}
	if timeout <= 0 {
		return pickDevice(list(), udid)
	}
	deadline := time.Now().Add(timeout)
	var last muxDevice
	var found bool
	for {
		devs := list()
		if d, ok := pickDevice(devs, udid); ok {
			last, found = d, true
			if d.Type == "Network" {
				return d, true
			}
		}
		if !time.Now().Before(deadline) {
			return last, found
		}
		time.Sleep(500 * time.Millisecond)
	}
}
