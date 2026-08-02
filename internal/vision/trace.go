package vision

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

type traceContextKey struct{}

type traceContext struct {
	requestID string
	ownerID   string
	callID    string
}

var readTraceRandom = rand.Read

func NewRequestTrace(ctx context.Context) (context.Context, string, error) {
	traceID, err := newOpaqueTraceID()
	if err != nil {
		return ctx, "", err
	}
	return context.WithValue(ctx, traceContextKey{}, traceContext{requestID: traceID}), traceID, nil
}

func ensureRequestTrace(ctx context.Context) (context.Context, error) {
	if requestTraceID(ctx) != "" {
		return ctx, nil
	}
	ctx, _, err := NewRequestTrace(ctx)
	return ctx, err
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

func newOpaqueTraceID() (string, error) {
	buffer := make([]byte, 12)
	if _, err := readTraceRandom(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}
