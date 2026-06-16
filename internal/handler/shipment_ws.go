package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"geo-service/internal/middleware"
	"geo-service/internal/model"
	"geo-service/internal/response"
	"geo-service/internal/service"
	"geo-service/internal/wsnearby"
)

// ShipmentWSHandler handles secure nearby lookups over WebSocket.
type ShipmentWSHandler struct {
	svc       *service.ShipmentService
	driverSvc *service.DriverService
	cfg       wsnearby.Config
	auth      middleware.WSAuthOptions
	limiter   *wsnearby.ConnLimiter
}

// NewShipmentWSHandler creates a WebSocket handler for shipment lookup.
func NewShipmentWSHandler(
	svc *service.ShipmentService,
	driverSvc *service.DriverService,
	cfg wsnearby.Config,
	auth middleware.WSAuthOptions,
) *ShipmentWSHandler {
	return &ShipmentWSHandler{
		svc:       svc,
		driverSvc: driverSvc,
		cfg:       cfg,
		auth:      auth,
		limiter:   wsnearby.NewConnLimiter(cfg.MaxPerIP, cfg.MaxGlobalConns),
	}
}

// HandleNearby handles GET /ws/shipments/nearby
//
//	@Summary		WebSocket - nearby shipments (secure)
//	@Description	Structured messages: {"type":"SUBSCRIBE_LOCATION","data":{"lat":..,"lng":..,"role":"passenger"}}. Legacy flat JSON supported when WS_SHIPMENT_LEGACY_FORMAT=true.
//	@Tags			websocket
//	@Success		101	"Switching Protocols - WebSocket upgrade successful"
//	@Failure		401	{object}	response.Failure	"Missing or invalid token"
//	@Failure		403	{object}	response.Failure	"Origin not allowed or insecure transport"
//	@Failure		429	{object}	response.Failure	"Too many connections"
//	@Failure		503	{object}	response.Failure	"Shipment database is not configured"
//	@Router			/ws/shipments/nearby [get]
func (h *ShipmentWSHandler) HandleNearby(c *gin.Context) {
	if h.svc == nil && h.driverSvc == nil {
		response.Fail(c, http.StatusServiceUnavailable, "SHIPMENT_SEARCH_DISABLED", service.ErrShipmentSearchDisabled.Error())
		return
	}

	ip := c.ClientIP()
	if h.cfg.RequireTLS && !middleware.RequestIsSecure(c.Request) {
		slog.Warn("ws shipment rejected: insecure transport", "ip", ip)
		response.Fail(c, http.StatusForbidden, "INSECURE_TRANSPORT", "secure WebSocket (WSS) is required")
		return
	}

	authResult, ok := middleware.AuthenticateWebSocketUpgrade(c.Request, h.auth)
	if !ok {
		slog.Warn("ws shipment auth failed", "ip", ip)
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "valid token is required")
		return
	}

	if !h.limiter.Acquire(ip) {
		slog.Warn("ws shipment connection limit exceeded", "ip", ip)
		response.Fail(c, http.StatusTooManyRequests, "TOO_MANY_CONNECTIONS", "connection limit exceeded for this client")
		return
	}
	released := false
	release := func() {
		if !released {
			released = true
			h.limiter.Release(ip)
		}
	}
	defer release()

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		slog.Warn("ws shipment upgrade failed", "ip", ip, "err", err)
		return
	}

	middleware.WSConnections.Inc()
	session := wsnearby.NewSession(conn, h.cfg, ip, authResult.UserID, func() {
		middleware.WSConnections.Dec()
		release()
	})
	defer session.Close()
	session.RunPumps()

	slog.Info("ws shipment connected", "ip", ip, "user_id", authResult.UserID, "auth", authResult.Method)
	if !session.WriteJSON(map[string]any{
		"type":    "connected",
		"channel": "shipment.nearby",
		"protocol": map[string]any{
			"version":  1,
			"messages": []string{wsnearby.MsgSubscribeLocation, wsnearby.MsgPing},
		},
	}) {
		return
	}

	if req, ok, err := shipmentRequestFromQuery(c); err != nil {
		session.WriteError("VALIDATION_ERROR", err.Error())
	} else if ok {
		h.process(session, c.Request.Context(), req)
	}

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				slog.Debug("ws shipment read closed", "ip", ip, "err", err)
			}
			return
		}
		if int64(len(message)) > h.cfg.MaxPayloadBytes {
			slog.Warn("ws shipment oversize payload", "ip", ip, "bytes", len(message))
			session.WriteError("PAYLOAD_TOO_LARGE", "message exceeds maximum size")
			return
		}
		if !session.AllowMessage() {
			slog.Warn("ws shipment rate limited", "ip", ip)
			session.WriteError("RATE_LIMITED", "too many messages; connection will close")
			return
		}

		msg, req, hasSearch, err := wsnearby.ParseInbound(message, h.cfg.AllowLegacy)
		if err != nil {
			session.WriteError("VALIDATION_ERROR", err.Error())
			continue
		}
		switch msg.Type {
		case wsnearby.MsgPing:
			session.WritePong()
		case wsnearby.MsgSubscribeLocation, "LEGACY":
			if hasSearch {
				h.process(session, c.Request.Context(), req)
			}
		default:
			session.WriteError("VALIDATION_ERROR", "unsupported message type")
		}
	}
}

func (h *ShipmentWSHandler) process(session *wsnearby.Session, parent context.Context, req model.NearbyShipmentRequest) {
	ctx, cancel := context.WithTimeout(parent, h.cfg.QueryTimeout)
	defer cancel()

	if nearbyRequestType(req.Type) == "sender" {
		if h.driverSvc == nil {
			session.WriteError("DRIVER_LOCATION_DISABLED", service.ErrDriverLocationDisabled.Error())
			return
		}
		result, err := h.driverSvc.SearchNearby(ctx, req.Lat, req.Lng, req.RadiusKm, req.Limit)
		if err != nil {
			if errors.Is(err, service.ErrDriverLocationDisabled) {
				session.WriteError("DRIVER_LOCATION_DISABLED", err.Error())
				return
			}
			slog.Warn("nearby driver search failed", "err", err)
			session.WriteError("DRIVER_SEARCH_FAILED", "driver search failed")
			return
		}
		session.WriteJSON(result)
		return
	}
	if nearbyRequestType(req.Type) == "" {
		session.WriteError("VALIDATION_ERROR", "unsupported nearby request type")
		return
	}
	if h.svc == nil {
		session.WriteError("SHIPMENT_SEARCH_DISABLED", service.ErrShipmentSearchDisabled.Error())
		return
	}

	result, err := h.svc.SearchNearby(ctx, req)
	if err != nil {
		if errors.Is(err, service.ErrShipmentSearchDisabled) {
			session.WriteError("SHIPMENT_SEARCH_DISABLED", err.Error())
			return
		}
		slog.Warn("nearby shipment search failed", "err", err)
		session.WriteError("SHIPMENT_SEARCH_FAILED", "shipment search failed")
		return
	}
	session.WriteJSON(result)
}

func shipmentRequestFromQuery(c *gin.Context) (model.NearbyShipmentRequest, bool, error) {
	latRaw := c.Query("lat")
	lngRaw := c.Query("lng")
	if latRaw == "" && lngRaw == "" {
		return model.NearbyShipmentRequest{}, false, nil
	}
	if latRaw == "" || lngRaw == "" {
		return model.NearbyShipmentRequest{}, false, errors.New("both lat and lng query parameters are required")
	}

	lat, err := strconv.ParseFloat(latRaw, 64)
	if err != nil {
		return model.NearbyShipmentRequest{}, false, errors.New("lat query parameter is invalid")
	}
	lng, err := strconv.ParseFloat(lngRaw, 64)
	if err != nil {
		return model.NearbyShipmentRequest{}, false, errors.New("lng query parameter is invalid")
	}

	req := model.NearbyShipmentRequest{Type: c.Query("type"), Lat: lat, Lng: lng}
	if radiusRaw := c.Query("radius_km"); radiusRaw != "" {
		radius, err := strconv.ParseFloat(radiusRaw, 64)
		if err != nil {
			return model.NearbyShipmentRequest{}, false, errors.New("radius_km query parameter is invalid")
		}
		req.RadiusKm = radius
	}
	if limitRaw := c.Query("limit"); limitRaw != "" {
		limit, err := strconv.Atoi(limitRaw)
		if err != nil {
			return model.NearbyShipmentRequest{}, false, errors.New("limit query parameter is invalid")
		}
		req.Limit = limit
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return model.NearbyShipmentRequest{}, false, err
	}
	_, validated, _, err := wsnearby.ParseInbound(payload, true)
	if err != nil {
		return model.NearbyShipmentRequest{}, false, err
	}
	return validated, true, nil
}

func nearbyRequestType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "passenger", "shipment.nearby":
		return "passenger"
	case "sender":
		return "sender"
	default:
		return ""
	}
}
