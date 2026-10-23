// Package ratelimit provides process-local tenant request limiting.
package ratelimit

import (
	"sync"
	"time"
)

// Decision describes the quota state after a request is evaluated.
type Decision struct {
	Limit     int
	Remaining int
	ResetAt   time.Time
}

type window struct {
	requests int
	resetAt  time.Time
}

// TenantLimiter applies the same fixed-window limit independently to each
// authenticated tenant. It is safe for concurrent use.
type TenantLimiter struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	tenants map[string]window
}

func NewTenantLimiter(limit int, duration time.Duration) *TenantLimiter {
	return NewTenantLimiterWithClock(limit, duration, time.Now)
}

// NewTenantLimiterWithClock makes window boundaries deterministic in tests.
func NewTenantLimiterWithClock(limit int, duration time.Duration, now func() time.Time) *TenantLimiter {
	if limit < 1 {
		limit = 1
	}
	if duration <= 0 {
		duration = time.Minute
	}
	if now == nil {
		now = time.Now
	}
	return &TenantLimiter{limit: limit, window: duration, now: now, tenants: make(map[string]window)}
}

// Allow records a request when quota remains and returns its resulting state.
func (l *TenantLimiter) Allow(tenantID string) (Decision, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	state, found := l.tenants[tenantID]
	if !found || !now.Before(state.resetAt) {
		state = window{resetAt: now.Add(l.window)}
	}
	if state.requests >= l.limit {
		return Decision{Limit: l.limit, Remaining: 0, ResetAt: state.resetAt}, false
	}
	state.requests++
	l.tenants[tenantID] = state
	return Decision{Limit: l.limit, Remaining: l.limit - state.requests, ResetAt: state.resetAt}, true
}
