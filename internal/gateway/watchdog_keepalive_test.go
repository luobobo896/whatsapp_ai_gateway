package gateway

import (
	"fmt"
	"os/exec"
	"runtime"
	"testing"
)

func TestChannelReachableForVia(t *testing.T) {
	if !channelReachableForVia(activateViaUSB, true, false, false) {
		t.Fatal("USB via needs USB")
	}
	if channelReachableForVia(activateViaUSB, false, true, true) {
		t.Fatal("USB via must not relaunch on Network/Wi-Fi")
	}
	if !channelReachableForVia(activateViaNetwork, false, true, false) {
		t.Fatal("Network via accepts usbmux Network")
	}
	if channelReachableForVia(activateViaNetwork, true, false, false) {
		t.Fatal("Network via must not relaunch on USB cable alone")
	}
	if runtime.GOOS == "windows" {
		t.Setenv(netmuxdEnvName, "tcp://127.0.0.1:27016")
		if channelReachableForVia(activateViaNetwork, false, false, true) {
			t.Fatal("Windows netmuxd must not reactivate on Wi-Fi IP alone (needs Network entry)")
		}
		if !channelReachableForVia(activateViaNetwork, false, true, false) {
			t.Fatal("Windows netmuxd accepts usbmux Network entry")
		}
		t.Setenv(netmuxdEnvName, "")
	}
	if !channelReachableForVia(activateViaNetwork, false, false, true) {
		t.Fatal("Network via accepts Wi-Fi IP (non-netmuxd path)")
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
