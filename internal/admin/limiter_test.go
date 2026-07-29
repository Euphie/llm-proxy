package admin

import (
	"fmt"
	"testing"
	"time"
)

// Break caught: allowing more than five attempts from one address or more than twenty attempts globally in a window.
func TestLoginLimiterAppliesAddressAndGlobalLimits(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	limiter := NewLoginLimiter(func() time.Time { return now })

	for i := 0; i < 5; i++ {
		if !limiter.Allow("192.0.2.1") {
			t.Fatalf("attempt %d unexpectedly denied", i+1)
		}
	}
	if limiter.Allow("192.0.2.1") {
		t.Fatal("sixth address attempt allowed")
	}
	for i := 0; i < 15; i++ {
		if !limiter.Allow(fmt.Sprintf("192.0.2.%d", i+2)) {
			t.Fatalf("global attempt %d unexpectedly denied", i+6)
		}
	}
	if limiter.Allow("198.51.100.1") {
		t.Fatal("twenty-first global attempt allowed")
	}
}

// Break caught: retaining address or global attempt counts after the fixed one-minute window expires.
func TestLoginLimiterResetsAddressAndGlobalLimitsAfterWindow(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	limiter := NewLoginLimiter(func() time.Time { return now })

	for i := 0; i < 5; i++ {
		if !limiter.Allow("192.0.2.1") {
			t.Fatalf("address attempt %d unexpectedly denied", i+1)
		}
	}
	for i := 0; i < 15; i++ {
		if !limiter.Allow(fmt.Sprintf("192.0.2.%d", i+2)) {
			t.Fatalf("global attempt %d unexpectedly denied", i+6)
		}
	}

	now = now.Add(time.Minute)
	if !limiter.Allow("192.0.2.1") {
		t.Fatal("address remained limited after window reset")
	}
	for i := 1; i < 20; i++ {
		if !limiter.Allow(fmt.Sprintf("198.51.100.%d", i)) {
			t.Fatalf("global attempt %d unexpectedly denied after reset", i+1)
		}
	}
	if limiter.Allow("203.0.113.1") {
		t.Fatal("twenty-first global attempt allowed after reset")
	}
}
