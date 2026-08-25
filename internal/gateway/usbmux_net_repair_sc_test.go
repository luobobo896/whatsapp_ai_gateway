package gateway

import (
	"errors"
	"strings"
	"testing"
)

func TestParseScQueryExistingRunning(t *testing.T) {
	out := `SERVICE_NAME: Apple Mobile Device Service
        TYPE               : 10  WIN32_OWN_PROCESS
        STATE              : 4  RUNNING
                                (STOPPABLE, NOT_PAUSABLE, IGNORES_SHUTDOWN)
`
	st, ok := parseScQuery(out)
	if !ok || st != 4 {
		t.Fatalf("got exists=%v state=%d", ok, st)
	}
}

func TestParseScQueryMissing1060(t *testing.T) {
	out := `[SC] OpenService 失败 1060:

指定的服务未安装。
`
	if _, ok := parseScQuery(out); ok {
		t.Fatal("1060 must not be treated as existing")
	}
}

func TestParseScQueryStopped(t *testing.T) {
	out := "STATE              : 1  STOPPED\n"
	st, ok := parseScQuery(out)
	if !ok || st != 1 {
		t.Fatalf("got exists=%v state=%d", ok, st)
	}
}

func TestFirstExistingServiceSkipsMissing(t *testing.T) {
	names := appleUsbmuxServiceCandidates()
	got := firstExistingService(names, map[string]bool{
		"Apple Devices":              true,
		"Apple Mobile Device Service": true,
	})
	if got != "Apple Mobile Device Service" {
		t.Fatalf("must pick real AMDS first, got %q", got)
	}
	if firstExistingService(names, nil) != "" {
		t.Fatal("none exist")
	}
}

func TestWindowsUsbmuxRestartBlockedMessage(t *testing.T) {
	if errWindowsUsbmuxRestartBlocked == nil || !strings.Contains(errWindowsUsbmuxRestartBlocked.Error(), "不会出现 ConnectionType=Network") {
		t.Fatalf("blocked error must explain why repair is refused: %v", errWindowsUsbmuxRestartBlocked)
	}
}

func TestScStartOKTreats1056AsSuccess(t *testing.T) {
	if !scStartOK(nil) {
		t.Fatal("nil err is ok")
	}
	if !scStartOK(errors.New("exit status 1056")) {
		t.Fatal("1056 already-running must be success")
	}
	if scStartOK(errors.New("exit status 1060")) {
		t.Fatal("1060 must not be success")
	}
}
