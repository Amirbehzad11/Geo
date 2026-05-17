package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"geo-service/internal/cache"
	"geo-service/internal/response"
)

// HealthHandler exposes the service health check endpoint.
type HealthHandler struct {
	redis *cache.Redis
}

// NewHealthHandler creates a HealthHandler that checks the given Redis client.
func NewHealthHandler(r *cache.Redis) *HealthHandler {
	return &HealthHandler{redis: r}
}

// Check handles GET /health
//
//	@Summary		Health check
//	@Description	Returns the service health status and the reachability of each downstream dependency. Returns HTTP 503 when any required dependency is unreachable.
//	@Tags			system
//	@Produce		json
//	@Success		200	{object}	response.Success	"All dependencies healthy"
//	@Failure		503	{object}	response.Failure	"One or more dependencies unreachable"
//	@Router			/health [get]
func (h *HealthHandler) Check(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	redisOK := h.redis.Ping(ctx) == nil

	if !redisOK {
		response.Fail(c, http.StatusServiceUnavailable, "DEGRADED", "redis unreachable")
		return
	}

	response.OK(c, gin.H{
		"status":  "ok",
		"service": "geo-service",
		"deps": gin.H{
			"redis": "ok",
		},
	})
}
