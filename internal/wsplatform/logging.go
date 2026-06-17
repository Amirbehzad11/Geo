package wsplatform

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

const (
	EventConnect      = "connect"
	EventDisconnect   = "disconnect"
	EventMessage      = "message"
	EventError        = "error"
	EventAuthFailed   = "auth_failed"
	EventRateLimited  = "rate_limit"
	EventConnRejected = "connection_rejected"
)

// LogEvent emits structured JSON logs compatible with Loki/Promtail pipelines.
func LogEvent(ctx context.Context, level slog.Level, channel, eventType, connectionID, clientIP string, userID int64, attrs ...any) {
	args := make([]any, 0, 16+len(attrs))
	args = append(args,
		"channel", channel,
		"event_type", eventType,
		"connection_id", connectionID,
		"client_ip", clientIP,
	)
	if userID > 0 {
		args = append(args, "user_id", userID)
	}
	if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
		args = append(args, "trace_id", span.SpanContext().TraceID().String())
	}
	args = append(args, attrs...)
	slog.Log(ctx, level, "websocket", args...)
}
