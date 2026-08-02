package vision

import (
	"context"
	"errors"
	"testing"
)

func TestNewRequestTraceFailsClosedWhenEntropyUnavailable(t *testing.T) {
	original := readTraceRandom
	readTraceRandom = func([]byte) (int, error) {
		return 0, errors.New("entropy unavailable")
	}
	t.Cleanup(func() { readTraceRandom = original })

	ctx, traceID, err := NewRequestTrace(context.Background())
	if err == nil || traceID != "" || requestTraceID(ctx) != "" {
		t.Fatalf("trace_id=%q context_trace=%q err=%v", traceID, requestTraceID(ctx), err)
	}
}
