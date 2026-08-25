//go:build windows

package gateway

import (
	"reflect"
	"testing"
)

func TestNetmuxdArgsShimMode(t *testing.T) {
	want := []string{"--port", "27016", "--upstream-usbmuxd", "127.0.0.1:27015"}
	if got := netmuxdArgs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("netmuxdArgs() = %#v, want %#v", got, want)
	}
}
