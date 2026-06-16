package handler

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"geo-service/internal/middleware"
	"geo-service/internal/response"
	"geo-service/internal/ws"
)

var allowedWebSocketOrigins = []string{"*"}

const (
	webSocketReadLimitBytes = 64 * 1024
	webSocketMessageLimit   = 30
	webSocketMessageWindow  = time.Minute
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	Subprotocols:    []string{"bearer"},
	CheckOrigin:     checkWebSocketOrigin,
}

// ConfigureWebSocketOrigins applies the same origin allowlist used by CORS.
// CORS does not protect WebSocket upgrades, so the upgrader must check Origin.
func ConfigureWebSocketOrigins(origins []string) {
	if len(origins) == 0 {
		allowedWebSocketOrigins = []string{"*"}
		return
	}
	allowedWebSocketOrigins = append([]string(nil), origins...)
}

func checkWebSocketOrigin(r *http.Request) bool {
	origin := strings.TrimRight(strings.TrimSpace(r.Header.Get("Origin")), "/")
	if origin == "" && websocketAllowsAnyOrigin() {
		return true
	}
	for _, allowed := range allowedWebSocketOrigins {
		allowed = strings.TrimRight(strings.TrimSpace(allowed), "/")
		if allowed == "*" || strings.EqualFold(origin, allowed) {
			return true
		}
		if originHostMatches(origin, allowed) {
			return true
		}
	}
	return false
}

func websocketAllowsAnyOrigin() bool {
	for _, allowed := range allowedWebSocketOrigins {
		if strings.TrimSpace(allowed) == "*" {
			return true
		}
	}
	return false
}

func originHostMatches(origin, allowed string) bool {
	originURL, err := url.Parse(origin)
	if err != nil || originURL.Host == "" {
		return false
	}
	allowedURL, err := url.Parse(allowed)
	if err == nil && allowedURL.Host != "" {
		return strings.EqualFold(originURL.Host, allowedURL.Host) && strings.EqualFold(originURL.Scheme, allowedURL.Scheme)
	}
	return strings.EqualFold(originURL.Host, allowed)
}

// WSHandler upgrades HTTP connections to WebSocket for live trip event streaming.
type WSHandler struct {
	hub         *ws.Hub
	authz       TripAuthorizer
	requireAuth bool
}

// NewWSHandler creates a WSHandler backed by the given hub.
func NewWSHandler(hub *ws.Hub, requireAuth bool, authorizers ...TripAuthorizer) *WSHandler {
	h := &WSHandler{hub: hub, requireAuth: requireAuth}
	if len(authorizers) > 0 {
		h.authz = authorizers[0]
	}
	return h
}

type TripAuthorizer interface {
	UserCanAccessTrip(ctx context.Context, userID, tripID int64) (bool, error)
}

// HandleConnection handles GET /ws/trip/:id
//
//	@Summary		WebSocket — live trip events
//	@Description	Upgrades the connection to WebSocket and streams real-time `location.updated` and `deviation.detected` events for the specified trip via Redis Pub/Sub. Send any message to keep the connection alive.
//	@Tags			websocket
//	@Param			id	path	int	true	"Trip ID"
//	@Success		101	"Switching Protocols — WebSocket upgrade successful"
//	@Failure		400	{object}	response.Failure	"Missing or invalid trip ID"
//	@Router			/ws/trip/{id} [get]
func (h *WSHandler) HandleConnection(c *gin.Context) {
	tripID := c.Param("id")
	if tripID == "" {
		response.Fail(c, http.StatusBadRequest, "INVALID_TRIP_ID", "trip id is required")
		return
	}
	tripIDNum, err := strconv.ParseInt(tripID, 10, 64)
	if err != nil || tripIDNum <= 0 {
		response.Fail(c, http.StatusBadRequest, "INVALID_TRIP_ID", "trip id must be a positive integer")
		return
	}
	if !h.authorizeTripRead(c, tripIDNum) {
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(webSocketReadLimitBytes)

	middleware.WSConnections.Inc()
	client := ws.NewClient(h.hub, conn, tripID, func() {
		middleware.WSConnections.Dec()
	})
	h.hub.Register(tripID, client)

	client.SendJSON(map[string]any{
		"type":    "connected",
		"trip_id": tripID,
	})

	go client.WritePump()
	go client.ReadPump()
}

func (h *WSHandler) authorizeTripRead(c *gin.Context, tripID int64) bool {
	if !h.requireAuth {
		return true
	}
	if middleware.AuthenticatedWithAPIKey(c) {
		return true
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "authenticated user is required")
		return false
	}
	if h.authz == nil {
		response.Fail(c, http.StatusServiceUnavailable, "AUTHORIZATION_UNAVAILABLE", "trip authorization is not configured")
		return false
	}
	allowed, err := h.authz.UserCanAccessTrip(c.Request.Context(), userID, tripID)
	if err != nil {
		slog.Error("trip websocket access check failed", "err", err, "trip_id", tripID, "user_id", userID)
		response.Fail(c, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error; please retry")
		return false
	}
	if !allowed {
		response.Fail(c, http.StatusForbidden, "FORBIDDEN", "trip access denied")
		return false
	}
	return true
}
