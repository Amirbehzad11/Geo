package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"geo-service/internal/response"
)

// APIKeyAuth returns a middleware that enforces X-API-Key header authentication.
// Mount this only when API_KEY_ENABLED=true; pass the expected key from config.
func APIKeyAuth(expectedKey string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetHeader("X-API-Key") != expectedKey {
			response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "invalid or missing API key")
			c.Abort()
			return
		}
		c.Next()
	}
}
