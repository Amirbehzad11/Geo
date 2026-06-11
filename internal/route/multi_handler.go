package route

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"geo-service/internal/response"
	"geo-service/internal/routing"
)

// MultiRouteHandler handles multi-waypoint route requests.
type MultiRouteHandler struct {
	svc   *MultiRouteService
	authz TripWriteAuthorizer
}

// NewMultiRouteHandler creates a MultiRouteHandler.
func NewMultiRouteHandler(svc *MultiRouteService, authorizers ...TripWriteAuthorizer) *MultiRouteHandler {
	h := &MultiRouteHandler{svc: svc}
	if len(authorizers) > 0 {
		h.authz = authorizers[0]
	}
	return h
}

// Calculate handles POST /route/waypoints
//
//	@Summary		Calculate a multi-stop route
//	@Description	Accepts a waypoint list (≥ 2) and a transport mode.
//	@Description	Keeps the first waypoint as the start, then reorders the rest
//	@Description	by nearest-neighbor (Haversine distance) before routing each
//	@Description	consecutive pair concurrently and stitching the polylines.
//	@Tags			routing
//	@Accept			json
//	@Produce		json
//	@Param			request	body		MultiRouteRequest								true	"Waypoints + mode"
//	@Success		200		{object}	response.Success{data=MultiRouteResponse}
//	@Failure		400		{object}	response.Failure
//	@Failure		422		{object}	response.Failure
//	@Failure		404		{object}	response.Failure
//	@Failure		503		{object}	response.Failure
//	@Router			/route/waypoints [post]
func (h *MultiRouteHandler) Calculate(c *gin.Context) {
	var req MultiRouteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}

	// Basic validation before hitting the engine.
	if len(req.Waypoints) < 2 {
		response.ValidationFail(c, "waypoints: at least 2 required")
		return
	}
	if len(req.Waypoints) > 50 {
		response.ValidationFail(c, "waypoints: maximum 50 allowed")
		return
	}
	for i, wp := range req.Waypoints {
		if !isValidLat(wp.Lat) || !isValidLng(wp.Lng) {
			response.ValidationFail(c, fmt.Sprintf(
				"waypoints[%d]: coordinates out of range (lat=%v lng=%v)", i, wp.Lat, wp.Lng))
			return
		}
	}
	if _, err := routing.NormalizeMode(req.Mode); err != nil {
		response.ValidationFail(c, err.Error())
		return
	}
	if !authorizeTripWrite(c, h.authz, req.TripID) {
		return
	}

	start := time.Now()
	resp, err := h.svc.Compute(c.Request.Context(), &req)
	elapsed := time.Since(start)

	if err != nil {
		status, code, msg := mapServiceError(err)
		slog.Error("multi-route calculation failed",
			"err", err, "waypoints", len(req.Waypoints), "mode", req.Mode)
		response.Fail(c, status, code, msg)
		return
	}

	slog.Info("multi-route request",
		"waypoints", len(req.Waypoints),
		"mode", req.Mode,
		"total_km", resp.TotalDistanceKm,
		"latency_ms", elapsed.Milliseconds(),
	)

	response.OK(c, resp)
}

func isValidLat(lat float64) bool { return lat >= -90 && lat <= 90 }
func isValidLng(lng float64) bool { return lng >= -180 && lng <= 180 }
