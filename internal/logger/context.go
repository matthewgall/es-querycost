package logger

import "context"

type traceIDKey struct{}
type clientIPKey struct{}

// WithTraceID attaches a trace ID to the context.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey{}, traceID)
}

// TraceIDFromContext retrieves the trace ID from the context.
func TraceIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(traceIDKey{}).(string); ok {
		return v
	}
	return ""
}

// WithClientIP attaches a resolved client IP to the context.
func WithClientIP(ctx context.Context, clientIP string) context.Context {
	return context.WithValue(ctx, clientIPKey{}, clientIP)
}

// ClientIPFromContext retrieves the resolved client IP from the context.
func ClientIPFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(clientIPKey{}).(string); ok {
		return v
	}
	return ""
}
