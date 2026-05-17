package middleware

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
)

// StructuredLogger returns a Gin middleware that emits one structured JSON log
// line per request using the stdlib slog default logger.
func StructuredLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		slog.Info("request",
			"method",     c.Request.Method,
			"path",       c.Request.URL.Path,
			"status",     c.Writer.Status(),
			"latency_ms", time.Since(start).Milliseconds(),
			"ip",         c.ClientIP(),
			"request_id", c.GetHeader("X-Request-ID"),
			"bytes_out",  c.Writer.Size(),
		)
	}
}
