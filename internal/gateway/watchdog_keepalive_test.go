package gateway

import (
	"fmt"
	"os/exec"
	"testing"
)

func TestChannelReachableForRelaunch(t *testing.T) {
	if !channelReachableForRelaunch(true, false) {
		t.Fatal("USB alone is enough to relaunch")
	}
	if !channelReachableForRelaunch(false, true) {
		t.Fatal("Wi-Fi alone is enough to relaunch (incl. 40-hex devices)")
	}
	if channelReachableForRelaunch(false, false) {
		t.Fatal("no USB and no Wi-Fi must not relaunch")
	}
}

func TestReactivateDecisionIgnoresUSBOnlyAssumption(t *testing.T) {
	// 健康失败 + 自动重激活 + 未在跑 + 仅 Wi-Fi 可达 → 应重激活（老机型也一样）
	if !reactivateDecision(false, true, false, true) {
		t.Fatal("reachable channel should allow reactivate")
	}
	if reactivateDecision(false, true, false, false) {
		t.Fatal("unreachable must not reactivate")
	}
	if reactivateDecision(true, true, false, true) {
		t.Fatal("healthy must not reactivate")
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
