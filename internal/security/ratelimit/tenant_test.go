package ratelimit

import (
	"testing"
	"time"
)

func TestTenantLimiter_GivenTenantQuota_WhenRequestsExceedLimit_ThenRejectsUntilWindowResets(t *testing.T) {
	now := time.Date(2026, time.October, 23, 12, 0, 0, 0, time.UTC)
	limiter := NewTenantLimiterWithClock(2, time.Minute, func() time.Time { return now })

	first, allowed := limiter.Allow("merchant_alpha")
	if !allowed || first.Remaining != 1 {
		t.Fatalf("first decision = %#v, allowed = %v", first, allowed)
	}
	second, allowed := limiter.Allow("merchant_alpha")
	if !allowed || second.Remaining != 0 {
		t.Fatalf("second decision = %#v, allowed = %v", second, allowed)
	}
	rejected, allowed := limiter.Allow("merchant_alpha")
	if allowed || rejected.Remaining != 0 || !rejected.ResetAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("rejected decision = %#v, allowed = %v", rejected, allowed)
	}

	now = now.Add(time.Minute)
	reset, allowed := limiter.Allow("merchant_alpha")
	if !allowed || reset.Remaining != 1 {
		t.Fatalf("reset decision = %#v, allowed = %v", reset, allowed)
	}
}

func TestTenantLimiter_GivenSeparateTenants_WhenOneQuotaIsExhausted_ThenOtherTenantIsAllowed(t *testing.T) {
	limiter := NewTenantLimiter(1, time.Minute)
	if _, allowed := limiter.Allow("merchant_alpha"); !allowed {
		t.Fatal("first alpha request was rejected")
	}
	if _, allowed := limiter.Allow("merchant_alpha"); allowed {
		t.Fatal("second alpha request was allowed")
	}
	if _, allowed := limiter.Allow("merchant_beta"); !allowed {
		t.Fatal("first beta request was rejected")
	}
}
