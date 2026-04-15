// Package provider defines overload detection primitives.
package provider

import (
	"bytes"
	"time"
)

// Rule describes one overload condition and its retry policy.
// An empty BodyContains matches any response body.
type Rule struct {
	Status       int
	BodyContains string
	MaxRetries   int
	RetryDelay   time.Duration
	RetryJitter  time.Duration
}

// Match returns the first rule whose status and body condition match,
// or nil if none match.
func Match(rules []Rule, statusCode int, body []byte) *Rule {
	for i, r := range rules {
		if r.Status != statusCode {
			continue
		}
		if r.BodyContains == "" || bytes.Contains(body, []byte(r.BodyContains)) {
			return &rules[i]
		}
	}
	return nil
}
