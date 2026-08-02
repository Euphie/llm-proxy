package vision

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync/atomic"
)

type traceContextKey struct{}

type traceContext struct {
	requestID string
	ownerID   string
	callID    string
}

var fallbackTraceSequence atomic.Uint64

func NewRequestTrace(ctx context.Context) (context.Context, string) {
	traceID := newOpaqueTraceID()
	return context.WithValue(ctx, traceContextKey{}, traceContext{requestID: traceID}), traceID
}

func ensureRequestTrace(ctx context.Context) context.Context {
	if requestTraceID(ctx) != "" {
		return ctx
	}
	ctx, _ = NewRequestTrace(ctx)
	return ctx
}

func requestTraceID(ctx context.Context) string {
	trace, _ := ctx.Value(traceContextKey{}).(traceContext)
	return trace.requestID
}

func withLoadTrace(ctx context.Context, callID, ownerID string) context.Context {
	return context.WithValue(ctx, traceContextKey{}, traceContext{
		requestID: ownerID,
		ownerID:   ownerID,
		callID:    callID,
	})
}

func traceFromContext(ctx context.Context) traceContext {
	trace, _ := ctx.Value(traceContextKey{}).(traceContext)
	return trace
}

func newOpaqueTraceID() string {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err == nil {
		return hex.EncodeToString(buffer)
	}
	return fmt.Sprintf("local-%d", fallbackTraceSequence.Add(1))
}
