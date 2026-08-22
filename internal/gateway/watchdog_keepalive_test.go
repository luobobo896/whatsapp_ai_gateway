package gateway

import (
	"fmt"
	"os/exec"
	"testing"
)

func TestCannotLaunchWDAWithoutUSB(t *testing.T) {
	legacy := "4886579a97a96bad83b527862bab409b5a07c741"
	modern := "00008120-000865D90A10C01E"
	if !cannotLaunchWDAWithoutUSB(legacy, false) {
		t.Fatal("40-hex without USB cannot start WDA")
	}
	if cannotLaunchWDAWithoutUSB(legacy, true) {
		t.Fatal("40-hex with USB can start WDA")
	}
	if cannotLaunchWDAWithoutUSB(modern, false) {
		t.Fatal("iOS 16+ can start over Wi-Fi pairing; do not block")
	}
}

func TestHostProcDetachExpected(t *testing.T) {
	if hostProcDetachExpected(nil) {
		t.Fatal("nil is not a detach")
	}
	cmd := exec.Command("sh", "-c", "exit 75")
	err := cmd.Run()
	if !hostProcDetachExpected(err) {
		t.Fatalf("exit 75 should be USB detach, got %v", err)
	}
	cmd = exec.Command("sh", "-c", "exit 1")
	err = cmd.Run()
	if hostProcDetachExpected(err) {
		t.Fatalf("exit 1 is not USB detach, got %v", err)
	}
	if !hostProcDetachExpected(fmt.Errorf("xcodebuild: device was disconnected")) {
		t.Fatal("disconnect text should count")
	}
}

func TestCheckWDAEmptyIPWithoutTunnel(t *testing.T) {
	g := &Gateway{}
	h := g.checkWDA(&Device{UDID: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", Port: 8100})
	if h.OK {
		t.Fatal("empty IP and no tunnel must not be healthy")
	}
	if h.Error == "" {
		t.Fatal("empty IP should return an error")
	}
}
