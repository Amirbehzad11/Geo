package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"geo-service/internal/response"
)

const defaultBodyLimitBytes int64 = 1 << 20

// RequestBodyLimit rejects oversized request bodies before JSON binding.
func RequestBodyLimit(limitBytes int64) gin.HandlerFunc {
	if limitBytes <= 0 {
		limitBytes = defaultBodyLimitBytes
	}
	return func(c *gin.Context) {
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limitBytes)
		}
		c.Next()
	}
}

// IPRateLimit is a small fixed-window limiter for edge protection. Deployments
// with multiple replicas should still enforce a shared limit at the gateway.
func IPRateLimit(limitPerMinute int) gin.HandlerFunc {
	if limitPerMinute <= 0 {
		return func(c *gin.Context) { c.Next() }
	}

	type bucket struct {
		windowStart time.Time
		count       int
	}

	var (
		mu      sync.Mutex
		buckets = make(map[string]bucket)
	)
	window := time.Minute
	lastCleanup := time.Now()

	return func(c *gin.Context) {
		now := time.Now()
		ip := c.ClientIP()

		mu.Lock()
		if now.Sub(lastCleanup) > window {
			for key, b := range buckets {
				if now.Sub(b.windowStart) > 2*window {
					delete(buckets, key)
				}
			}
			lastCleanup = now
		}

		b := buckets[ip]
		if b.windowStart.IsZero() || now.Sub(b.windowStart) >= window {
			b = bucket{windowStart: now}
		}
		b.count++
		buckets[ip] = b
		allowed := b.count <= limitPerMinute
		mu.Unlock()

		if !allowed {
			response.Fail(c, http.StatusTooManyRequests, "RATE_LIMITED", "too many requests; please retry later")
			c.Abort()
			return
		}
		c.Next()
	}
}
