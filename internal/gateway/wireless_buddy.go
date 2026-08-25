package gateway

import "strings"

// needsWirelessRebind 本机 HostID 与手机 WirelessBuddyID 不一致（或 Buddy 为空）时必须从本机重绑。
// 只勾 iTunes「Wi-Fi 同步」不能当成已经 Network。
func needsWirelessRebind(buddy, hostID string) bool {
	h := strings.TrimSpace(strings.ToUpper(hostID))
	if h == "" {
		return true
	}
	return strings.TrimSpace(strings.ToUpper(buddy)) != h
}

// preferredWirelessBuddyID Windows 上 iTunes 的 WirelessBuddyID 与 pair HostID 不是同一个；
// AMDS/iTunes Wi-Fi 同步认的是 iTunes 那个。
func preferredWirelessBuddyID(itunesBuddy, pairHostID string) string {
	if s := strings.TrimSpace(itunesBuddy); s != "" {
		return s
	}
	return strings.TrimSpace(pairHostID)
}
