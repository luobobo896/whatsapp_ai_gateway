package gateway

import (
	"strings"
	"testing"
)

func TestNeedsWirelessRebind(t *testing.T) {
	host := "31273522-368570121651719656"
	if !needsWirelessRebind("514FF52E-F27B-4EF0-91EB-B36FE1F0D23C", host) {
		t.Fatal("Mac buddy vs this Windows HostID must re-bind")
	}
	if !needsWirelessRebind("", host) {
		t.Fatal("empty buddy must re-bind")
	}
	if !needsWirelessRebind("x", "") {
		t.Fatal("empty host id must re-bind")
	}
	if needsWirelessRebind(host, host) {
		t.Fatal("same host must not re-bind")
	}
	if needsWirelessRebind(strings.ToLower(host), host) {
		t.Fatal("case-insensitive match must not re-bind")
	}
}

func TestPreferredWirelessBuddyIDPrefersITunes(t *testing.T) {
	itunes := "31274029-17647842852914356048"
	pair := "31273522-368570121651719656"
	if got := preferredWirelessBuddyID(itunes, pair); got != itunes {
		t.Fatalf("got %q want iTunes buddy", got)
	}
	if got := preferredWirelessBuddyID("", pair); got != pair {
		t.Fatalf("empty iTunes falls back to pair HostID, got %q", got)
	}
	if !needsWirelessRebind(pair, preferredWirelessBuddyID(itunes, pair)) {
		t.Fatal("phone bound to pair HostID must re-bind to iTunes buddy")
	}
}
