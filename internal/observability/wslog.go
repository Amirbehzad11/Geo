package observability

import (
	"context"
	"log/slog"
)

const (
	WSEventConnect      = "connect"
	WSEventDisconnect   = "disconnect"
	WSEventMessage      = "message"
	WSEventError        = "error"
	WSEventAuthFailed   = "auth_failed"
	WSEventRateLimited  = "rate_limited"
	WSEventConnRejected = "connection_rejected"
)

// LogWSEvent emits a structured JSON log line for WebSocket lifecycle events.
func LogWSEvent(level slog.Level, channel, event, connectionID, clientIP string, userID int64, attrs ...any) {
	args := make([]any, 0, 12+len(attrs))
	args = append(args,
		"channel", channel,
		"ws_event", event,
		"connection_id", connectionID,
		"client_ip", clientIP,
	)
	if userID > 0 {
		args = append(args, "user_id", userID)
	}
	args = append(args, attrs...)
	slog.Log(context.Background(), level, "websocket", args...)
}
