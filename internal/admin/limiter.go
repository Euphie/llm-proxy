package admin

import (
	"sync"
	"time"
)

const (
	perAddressAttempts = 5
	globalAttempts     = 20
	loginWindow        = time.Minute
)

type LoginLimiter struct {
	mu            sync.Mutex
	now           func() time.Time
	windowStarted time.Time
	addressCounts map[string]int
	globalCount   int
}

func NewLoginLimiter(now func() time.Time) *LoginLimiter {
	return &LoginLimiter{
		now:           now,
		addressCounts: make(map[string]int),
	}
}

func (l *LoginLimiter) Allow(address string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if l.windowStarted.IsZero() || now.Sub(l.windowStarted) >= loginWindow || now.Before(l.windowStarted) {
		l.windowStarted = now
		l.addressCounts = make(map[string]int)
		l.globalCount = 0
	}
	if l.addressCounts[address] >= perAddressAttempts || l.globalCount >= globalAttempts {
		return false
	}
	l.addressCounts[address]++
	l.globalCount++
	return true
}
