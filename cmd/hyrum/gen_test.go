package main

import (
	"testing"

	"github.com/alpha-omega-security/hyrum/internal/hyrum"
)

func TestAnyRan(t *testing.T) {
	if anyRan(nil) {
		t.Error("nil slice")
	}
	if anyRan([]hyrum.VerifyResult{{Error: "install failed"}}) {
		t.Error("error-only should not count as ran")
	}
	if !anyRan([]hyrum.VerifyResult{{Version: "1.0", Pass: 3}}) {
		t.Error("pass counts as ran")
	}
	if !anyRan([]hyrum.VerifyResult{{Error: "x"}, {Version: "2.0", Fail: 1}}) {
		t.Error("mixed: one ran")
	}
}
