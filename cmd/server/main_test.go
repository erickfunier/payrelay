package main

import (
	"testing"
	"time"
)

func TestConfigurationFallbacksAndOverrides(t *testing.T) {
	t.Setenv("PAYRELAY_TEST_STRING", "configured")
	if got := env("PAYRELAY_TEST_STRING", "fallback"); got != "configured" {
		t.Fatalf("env override = %q", got)
	}
	if got := env("PAYRELAY_TEST_MISSING", "fallback"); got != "fallback" {
		t.Fatalf("env fallback = %q", got)
	}
	t.Setenv("PAYRELAY_TEST_DURATION", "2s")
	if got := durationEnv("PAYRELAY_TEST_DURATION", time.Second); got != 2*time.Second {
		t.Fatalf("duration override = %s", got)
	}
	for _, value := range []string{"", "invalid", "0s", "-1s"} {
		t.Setenv("PAYRELAY_TEST_DURATION", value)
		if got := durationEnv("PAYRELAY_TEST_DURATION", time.Second); got != time.Second {
			t.Fatalf("duration %q = %s", value, got)
		}
	}
	t.Setenv("PAYRELAY_TEST_INT", "7")
	if got := intEnv("PAYRELAY_TEST_INT", 3); got != 7 {
		t.Fatalf("int override = %d", got)
	}
	for _, value := range []string{"", "invalid", "0", "-2"} {
		t.Setenv("PAYRELAY_TEST_INT", value)
		if got := intEnv("PAYRELAY_TEST_INT", 3); got != 3 {
			t.Fatalf("int %q = %d", value, got)
		}
	}
}
