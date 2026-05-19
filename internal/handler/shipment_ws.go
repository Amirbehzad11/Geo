package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"geo-service/internal/middleware"
	"geo-service/internal/model"
	"geo-service/internal/response"
	"geo-service/internal/service"
	"geo-service/internal/utils"
)

const shipmentQueryTimeout = 10 * time.Second

// ShipmentWSHandler handles nearby lookups over WebSocket.
type ShipmentWSHandler struct {
	svc       *service.ShipmentService
	driverSvc *service.DriverService
}

// NewShipmentWSHandler creates a WebSocket handler for shipment lookup.
func NewShipmentWSHandler(svc *service.ShipmentService, driverSvc *service.DriverService) *ShipmentWSHandler {
	return &ShipmentWSHandler{svc: svc, driverSvc: driverSvc}
}

// HandleNearby handles GET /ws/shipments/nearby
//
//	@Summary		WebSocket - nearby shipments
//	@Description	Receives lat/lng over WebSocket. type=passenger queries the Laravel shipment table; type=sender searches nearby passengers in Redis.
//	@Tags			websocket
//	@Success		101	"Switching Protocols - WebSocket upgrade successful"
//	@Failure		503	{object}	response.Failure	"Shipment database is not configured"
//	@Router			/ws/shipments/nearby [get]
func (h *ShipmentWSHandler) HandleNearby(c *gin.Context) {
	if h.svc == nil && h.driverSvc == nil {
		response.Fail(c, http.StatusServiceUnavailable, "SHIPMENT_SEARCH_DISABLED", "shipment search database is not configured")
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	middleware.WSConnections.Inc()
	defer middleware.WSConnections.Dec()

	if err := conn.WriteJSON(map[string]any{
		"type":    "connected",
		"channel": "shipment.nearby",
	}); err != nil {
		return
	}

	if req, ok, err := shipmentRequestFromQuery(c); err != nil {
		writeShipmentWSError(conn, "VALIDATION_ERROR", err.Error())
	} else if ok {
		if !h.process(conn, c.Request.Context(), req) {
			return
		}
	}

	for {
		var req model.NearbyShipmentRequest
		messageType, message, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
			continue
		}
		if err := json.Unmarshal(message, &req); err != nil {
			if !writeShipmentWSError(conn, "INVALID_JSON", "message must be valid JSON") {
				return
			}
			continue
		}
		if !h.process(conn, c.Request.Context(), req) {
			return
		}
	}
}

func (h *ShipmentWSHandler) process(conn *websocket.Conn, parent context.Context, req model.NearbyShipmentRequest) bool {
	if !utils.ValidCoords(req.Lat, req.Lng) {
		return writeShipmentWSError(conn, "VALIDATION_ERROR", "coordinates out of valid range (-90 <= lat <= 90, -180 <= lng <= 180)")
	}
	if req.RadiusKm < 0 {
		return writeShipmentWSError(conn, "VALIDATION_ERROR", "radius_km must be positive")
	}
	if req.Limit < 0 {
		return writeShipmentWSError(conn, "VALIDATION_ERROR", "limit must be positive")
	}

	ctx, cancel := context.WithTimeout(parent, shipmentQueryTimeout)
	defer cancel()

	if nearbyRequestType(req.Type) == "sender" {
		if h.driverSvc == nil {
			return writeShipmentWSError(conn, "DRIVER_LOCATION_DISABLED", service.ErrDriverLocationDisabled.Error())
		}
		result, err := h.driverSvc.SearchNearby(ctx, req.Lat, req.Lng, req.RadiusKm, req.Limit)
		if err != nil {
			if errors.Is(err, service.ErrDriverLocationDisabled) {
				return writeShipmentWSError(conn, "DRIVER_LOCATION_DISABLED", err.Error())
			}
			return writeShipmentWSError(conn, "DRIVER_SEARCH_FAILED", err.Error())
		}
		return conn.WriteJSON(result) == nil
	}
	if nearbyRequestType(req.Type) == "" {
		return writeShipmentWSError(conn, "VALIDATION_ERROR", "unsupported nearby request type")
	}
	if h.svc == nil {
		return writeShipmentWSError(conn, "SHIPMENT_SEARCH_DISABLED", service.ErrShipmentSearchDisabled.Error())
	}

	result, err := h.svc.SearchNearby(ctx, req)
	if err != nil {
		if errors.Is(err, service.ErrShipmentSearchDisabled) {
			return writeShipmentWSError(conn, "SHIPMENT_SEARCH_DISABLED", err.Error())
		}
		return writeShipmentWSError(conn, "SHIPMENT_SEARCH_FAILED", err.Error())
	}
	return conn.WriteJSON(result) == nil
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

	return req, true, nil
}

func writeShipmentWSError(conn *websocket.Conn, code, message string) bool {
	return conn.WriteJSON(map[string]any{
		"type":         "error",
		"code":         code,
		"message":      message,
		"timestamp_ms": time.Now().UnixMilli(),
	}) == nil
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
