// Package provider defines upstream failure and overload detection primitives.
package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

type FailureClass string

const (
	FailureAuthentication            FailureClass = "authentication"
	FailureRequestProtocolCapability FailureClass = "request_protocol_capability"
	FailureOverloadTransient         FailureClass = "overload_transient"
	FailureOperationTimeout          FailureClass = "operation_timeout"
	FailureBudgetDeadline            FailureClass = "budget_deadline"
	FailureUnknownTransport          FailureClass = "unknown_transport"
	FailureMalformedResponse         FailureClass = "malformed_response"
)

type Failure struct {
	Class      FailureClass
	StatusCode int
	cause      error
}

func NewFailure(class FailureClass, statusCode int, cause error) *Failure {
	return &Failure{Class: class, StatusCode: statusCode, cause: cause}
}

func (e *Failure) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("upstream %s failure: status %d", e.Class, e.StatusCode)
	}
	return fmt.Sprintf("upstream %s failure", e.Class)
}

func (e *Failure) Unwrap() error { return e.cause }

func FailureClassOf(err error) (FailureClass, bool) {
	var failure *Failure
	if !errors.As(err, &failure) {
		return "", false
	}
	return failure.Class, true
}

func FailureStatus(err error) int {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure.StatusCode
	}
	return 0
}

func IsRetryableStatus(status int) bool {
	return status == 408 || status == 425 || status == 429 || status >= 500 && status <= 599
}

func ClassifyHTTPFailure(rules []Rule, statusCode int, body []byte, cause error) *Failure {
	class := FailureRequestProtocolCapability
	switch {
	case statusCode == 401 || statusCode == 403:
		class = FailureAuthentication
	case Match(rules, statusCode, body) != nil,
		statusCode == 429,
		statusCode >= 500 && statusCode <= 599:
		class = FailureOverloadTransient
	}
	return NewFailure(class, statusCode, cause)
}

func ClassifyTransportFailure(err error) *Failure {
	var existing *Failure
	if errors.As(err, &existing) {
		return existing
	}
	class := FailureUnknownTransport
	var networkError net.Error
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.As(err, &networkError) && networkError.Timeout() {
		class = FailureOperationTimeout
	}
	return NewFailure(class, 0, err)
}

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
		if !IsRetryableStatus(r.Status) {
			continue
		}
		if r.Status != statusCode {
			continue
		}
		if r.BodyContains == "" || bytes.Contains(body, []byte(r.BodyContains)) {
			return &rules[i]
		}
	}
	return nil
}

func FirstRetryRule(rules []Rule) *Rule {
	for i := range rules {
		if IsRetryableStatus(rules[i].Status) {
			return &rules[i]
		}
	}
	return nil
}
