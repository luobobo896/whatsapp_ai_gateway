package main

import "time"

// muxLockdownPort is go-ios Lockdownport (htons 62078 on little-endian).
const muxLockdownPortA uint16 = 32498
const muxLockdownPortB uint16 = 32500

type muxDevice struct {
	ID   int
	UDID string
	Type string
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
