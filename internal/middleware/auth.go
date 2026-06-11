package middleware

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"geo-service/internal/response"
)

const (
	authUserIDKey      = "auth_user_id"
	authMethodKey      = "auth_method"
	authMethodJWT      = "jwt"
	authMethodKeyValue = "api_key"
)

// AuthOptions configures request authentication. When multiple mechanisms are
// configured, any valid credential is accepted so existing API-key clients keep
// working while Laravel JWT clients migrate in.
type AuthOptions struct {
	APIKey       string
	JWTSecret    string
	JWTAlgorithm string
}

// Auth accepts either X-API-Key or a Laravel tymon/jwt-auth Bearer token.
func Auth(opts AuthOptions) gin.HandlerFunc {
	expectedAPIKey := []byte(strings.TrimSpace(opts.APIKey))
	jwtSecret := strings.TrimSpace(opts.JWTSecret)
	jwtAlgorithm := strings.ToUpper(strings.TrimSpace(opts.JWTAlgorithm))
	if jwtAlgorithm == "" {
		jwtAlgorithm = "HS256"
	}

	return func(c *gin.Context) {
		if len(expectedAPIKey) > 0 {
			provided := []byte(c.GetHeader("X-API-Key"))
			if subtle.ConstantTimeCompare(provided, expectedAPIKey) == 1 {
				c.Set(authMethodKey, authMethodKeyValue)
				c.Next()
				return
			}
		}

		if jwtSecret != "" {
			token := bearerToken(c)
			userID, err := validateJWT(token, jwtSecret, jwtAlgorithm)
			if token != "" && err == nil {
				c.Set(authMethodKey, authMethodJWT)
				c.Set(authUserIDKey, userID)
				c.Next()
				return
			}
		}

		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "invalid or missing credentials")
		c.Abort()
	}
}

// AuthenticatedUserID returns the Laravel user_id/sub carried by a valid JWT.
func AuthenticatedUserID(c *gin.Context) (int64, bool) {
	v, ok := c.Get(authUserIDKey)
	if !ok {
		return 0, false
	}
	id, ok := v.(int64)
	return id, ok && id > 0
}

// AuthenticatedWithAPIKey reports whether the request used the shared API key.
func AuthenticatedWithAPIKey(c *gin.Context) bool {
	v, ok := c.Get(authMethodKey)
	return ok && v == authMethodKeyValue
}

func bearerToken(c *gin.Context) string {
	auth := strings.TrimSpace(c.GetHeader("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[len("bearer "):])
	}

	if token := websocketSubprotocolBearer(c.GetHeader("Sec-WebSocket-Protocol")); token != "" {
		return token
	}
	return ""
}

func websocketSubprotocolBearer(raw string) string {
	parts := strings.Split(raw, ",")
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if strings.EqualFold(part, "bearer") && i+1 < len(parts) {
			return strings.TrimSpace(parts[i+1])
		}
	}
	return ""
}

func validateJWT(raw, secret, algorithm string) (int64, error) {
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		if token.Method == nil || token.Method.Alg() != algorithm {
			return nil, fmt.Errorf("unexpected jwt alg %q", token.Header["alg"])
		}
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("jwt alg must be HMAC")
		}
		return []byte(secret), nil
	}, jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithLeeway(30*time.Second))
	if err != nil {
		return 0, err
	}
	if token == nil || !token.Valid {
		return 0, errors.New("invalid jwt")
	}
	userID, ok := jwtClaimInt64(claims["user_id"])
	if !ok {
		userID, ok = jwtClaimInt64(claims["sub"])
	}
	if !ok || userID <= 0 {
		return 0, errors.New("missing user identity claim")
	}
	return userID, nil
}

func jwtClaimInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case float64:
		return int64(x), x > 0
	case int64:
		return x, x > 0
	case json.Number:
		id, err := x.Int64()
		return id, err == nil && id > 0
	case string:
		id, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return id, err == nil && id > 0
	default:
		return 0, false
	}
}
