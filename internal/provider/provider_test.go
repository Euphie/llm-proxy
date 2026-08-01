package provider

import (
	"context"
	"errors"
	"testing"
)

func TestTransportClassificationSeparatesOperationTimeoutFromHardBudgetFailure(t *testing.T) {
	timeout := ClassifyTransportFailure(context.DeadlineExceeded)
	if timeout.Class != FailureClass("operation_timeout") {
		t.Fatalf("timeout class=%q, want operation_timeout", timeout.Class)
	}

	budget := NewFailure(FailureBudgetDeadline, 0, errors.New("request budget exhausted"))
	classifiedBudget := ClassifyTransportFailure(budget)
	if classifiedBudget.Class != FailureBudgetDeadline {
		t.Fatalf("budget class=%q, want %q", classifiedBudget.Class, FailureBudgetDeadline)
	}
}
