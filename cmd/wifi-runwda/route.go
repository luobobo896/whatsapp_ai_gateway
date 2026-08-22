package main

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
