package delivery

import (
	"testing"
	"time"
)

func TestRetryAfterIsBoundedAndExponential(t *testing.T) {
	for attempt := 1; attempt <= 20; attempt++ {
		delay := RetryAfter(attempt)
		if delay < time.Second {
			t.Fatalf("attempt %d: delay %s below base backoff", attempt, delay)
		}
		if delay > maxBackoff+maxBackoff/4 {
			t.Fatalf("attempt %d: delay %s exceeds capped jitter", attempt, delay)
		}
	}
	first := RetryAfter(1)
	later := RetryAfter(4)
	if later < first {
		t.Fatalf("expected later attempt to be no shorter: %s < %s", later, first)
	}
}
