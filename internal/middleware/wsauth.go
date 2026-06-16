package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// WSAuthOptions configures WebSocket upgrade authentication.
type WSAuthOptions struct {
	RequireAuth  bool
	APIKey       string
	JWTSecret    string
	JWTAlgorithm string
}

// WSAuthResult describes a validated WebSocket credential.
type WSAuthResult struct {
	UserID int64
	Method string // "jwt" | "api_key"
}

// AuthenticateWebSocketUpgrade validates credentials on a WebSocket handshake.
// Tokens may be sent via Authorization, Sec-WebSocket-Protocol (bearer, TOKEN),
// or ?token= query parameter.
func AuthenticateWebSocketUpgrade(r *http.Request, opts WSAuthOptions) (WSAuthResult, bool) {
	if !opts.RequireAuth {
		return WSAuthResult{}, true
	}

	if key := strings.TrimSpace(opts.APIKey); key != "" {
		provided := []byte(r.Header.Get("X-API-Key"))
		expected := []byte(key)
		if len(provided) > 0 && subtle.ConstantTimeCompare(provided, expected) == 1 {
			return WSAuthResult{Method: authMethodKeyValue}, true
		}
	}

	token := wsBearerToken(r)
	if token == "" {
		return WSAuthResult{}, false
	}

	if key := strings.TrimSpace(opts.APIKey); key != "" && subtle.ConstantTimeCompare([]byte(token), []byte(key)) == 1 {
		return WSAuthResult{Method: authMethodKeyValue}, true
	}

	secret := strings.TrimSpace(opts.JWTSecret)
	if secret == "" {
		return WSAuthResult{}, false
	}
	alg := strings.ToUpper(strings.TrimSpace(opts.JWTAlgorithm))
	if alg == "" {
		alg = "HS256"
	}
	userID, err := validateJWT(token, secret, alg)
	if err != nil || userID <= 0 {
		return WSAuthResult{}, false
	}
	return WSAuthResult{UserID: userID, Method: authMethodJWT}, true
}

func wsBearerToken(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[len("bearer "):])
	}
	if token := websocketSubprotocolBearer(r.Header.Get("Sec-WebSocket-Protocol")); token != "" {
		return token
	}
	return strings.TrimSpace(r.URL.Query().Get("token"))
}

// RequestIsSecure reports whether the handshake arrived over TLS (direct or via proxy).
func RequestIsSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))) {
	case "https", "wss":
		return true
	default:
		return false
	}
}
