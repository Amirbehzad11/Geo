package middleware

import (
	"crypto/subtle"
	"net/http"

	"github.com/gin-gonic/gin"

	"geo-service/internal/response"
)

// APIKeyAuth returns a middleware that enforces X-API-Key header authentication.
// Mount this only when API_KEY_ENABLED=true; pass the expected key from config.
// Uses constant-time comparison to prevent timing-based key recovery attacks.
func APIKeyAuth(expectedKey string) gin.HandlerFunc {
	expected := []byte(expectedKey)
	return func(c *gin.Context) {
		provided := []byte(c.GetHeader("X-API-Key"))
		if subtle.ConstantTimeCompare(provided, expected) != 1 {
			response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "invalid or missing API key")
			c.Abort()
			return
		}
		c.Next()
	}
}
