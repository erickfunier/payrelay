package application

import (
	"math/rand/v2"
	"time"
)

const (
	baseBackoff = time.Second
	maxBackoff  = 5 * time.Minute
)

// RetryAfter uses capped exponential backoff plus up to 25% full jitter.
func RetryAfter(failedAttempt int) time.Duration {
	backoff := baseBackoff
	for i := 1; i < failedAttempt && backoff < maxBackoff; i++ {
		backoff *= 2
	}
	if backoff > maxBackoff {
		backoff = maxBackoff
	}
	return backoff + time.Duration(rand.Int64N(int64(backoff)/4+1))
}
