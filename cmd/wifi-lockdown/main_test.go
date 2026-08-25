package main

import (
	"errors"
	"strings"
	"testing"
)

func TestIsPasscodeRequiredErr(t *testing.T) {
	if isPasscodeRequiredErr(nil) {
		t.Fatal("nil")
	}
	if !isPasscodeRequiredErr(errors.New("Failed setting 'EnableWifiDebugging' to '%!s(bool=true)' with err: PasscodeRequired")) {
		t.Fatal("PasscodeRequired")
	}
	if !isPasscodeRequiredErr(errors.New("PasswordProtected")) {
		t.Fatal("PasswordProtected")
	}
	if !isPasscodeRequiredErr(errors.New("lockdown 0xe80000ee")) {
		t.Fatal("0xe80000ee")
	}
	if isPasscodeRequiredErr(errors.New("get device: not found")) {
		t.Fatal("other error")
	}
}

func TestBoolTrue(t *testing.T) {
	if !boolTrue(true) || boolTrue(false) {
		t.Fatal("bool")
	}
	if !boolTrue("true") || !boolTrue("1") || boolTrue("false") {
		t.Fatal("string")
	}
}

func TestPasscodeTimeoutErrToken(t *testing.T) {
	err := passcodeTimeoutErr()
	if !strings.Contains(err.Error(), needPasscodeToken) {
		t.Fatalf("%v", err)
	}
	if strings.Contains(err.Error(), "0000") {
		t.Fatal("must not mention a sample passcode")
	}
}
