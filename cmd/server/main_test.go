package main

import (
	"testing"
	"time"
)

func TestConfiguration_GivenOverridesAndFallbacks_WhenRead_ThenReturnsExpectedValues(t *testing.T) {
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
	for _, tt := range []struct {
		name  string
		value string
	}{
		{name: "given_empty_duration_when_read_then_uses_fallback", value: ""},
		{name: "given_malformed_duration_when_read_then_uses_fallback", value: "invalid"},
		{name: "given_zero_duration_when_read_then_uses_fallback", value: "0s"},
		{name: "given_negative_duration_when_read_then_uses_fallback", value: "-1s"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PAYRELAY_TEST_DURATION", tt.value)
			if got := durationEnv("PAYRELAY_TEST_DURATION", time.Second); got != time.Second {
				t.Fatalf("duration %q = %s", tt.value, got)
			}
		})
	}
	t.Setenv("PAYRELAY_TEST_INT", "7")
	if got := intEnv("PAYRELAY_TEST_INT", 3); got != 7 {
		t.Fatalf("int override = %d", got)
	}
	for _, tt := range []struct {
		name  string
		value string
	}{
		{name: "given_empty_integer_when_read_then_uses_fallback", value: ""},
		{name: "given_malformed_integer_when_read_then_uses_fallback", value: "invalid"},
		{name: "given_zero_integer_when_read_then_uses_fallback", value: "0"},
		{name: "given_negative_integer_when_read_then_uses_fallback", value: "-2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PAYRELAY_TEST_INT", tt.value)
			if got := intEnv("PAYRELAY_TEST_INT", 3); got != 3 {
				t.Fatalf("int %q = %d", tt.value, got)
			}
		})
	}
}
