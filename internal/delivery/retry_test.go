package delivery

import (
	"testing"
	"time"
)

func TestRetry_GivenFailureAttempt_WhenDelayIsCalculated_ThenStaysWithinBackoffBounds(t *testing.T) {
	cases := []struct {
		name    string
		attempt int
	}{
		{name: "given_first_failure_when_calculated_then_uses_base_backoff", attempt: 1},
		{name: "given_midstream_failure_when_calculated_then_grows_exponentially", attempt: 4},
		{name: "given_many_failures_when_calculated_then_caps_backoff", attempt: 20},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			delay := RetryAfter(tt.attempt)
			if delay < time.Second {
				t.Fatalf("attempt %d: delay %s below base backoff", tt.attempt, delay)
			}
			if delay > maxBackoff+maxBackoff/4 {
				t.Fatalf("attempt %d: delay %s exceeds capped jitter", tt.attempt, delay)
			}
		})
	}
	first := RetryAfter(1)
	later := RetryAfter(4)
	if later < first {
		t.Fatalf("expected later attempt to be no shorter: %s < %s", later, first)
	}
}
