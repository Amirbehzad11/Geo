package handler

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"geo-service/internal/middleware"
	"geo-service/internal/model"
	"geo-service/internal/response"
	"geo-service/internal/routing"
	"geo-service/internal/service"
	"geo-service/internal/utils"
)

// RouteHandler exposes the route-calculation endpoint.
type RouteHandler struct {
	svc *service.RouteService
}

// NewRouteHandler creates a RouteHandler backed by the given service.
func NewRouteHandler(svc *service.RouteService) *RouteHandler {
	return &RouteHandler{svc: svc}
}

// Calculate handles POST /route
//
//	@Summary		Calculate a route
//	@Description	Computes the fastest route between two coordinates. Uses the configured routing backend (OSRM or internal A*), with automatic fallback to the internal engine when the primary backend is unavailable. Falls back to a straight Haversine estimate when endpoints lie outside graph coverage.
//	@Tags			routing
//	@Accept			json
//	@Produce		json
//	@Param			request	body		model.RouteRequest								true	"Route request"
//	@Success		200		{object}	response.Success{data=model.RouteResponse}		"Route calculated successfully"
//	@Failure		400		{object}	response.Failure								"Malformed JSON body"
//	@Failure		422		{object}	response.Failure								"Validation error (invalid coordinates or transport mode)"
//	@Failure		503		{object}	response.Failure								"Routing engine overloaded — too many concurrent requests"
//	@Failure		500		{object}	response.Failure								"Internal routing error"
//	@Router			/route [post]
func (h *RouteHandler) Calculate(c *gin.Context) {
	var req model.RouteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}

	if !utils.ValidCoords(req.StartLat, req.StartLng) {
		response.ValidationFail(c, "start coordinates out of valid range (-90 ≤ lat ≤ 90, -180 ≤ lng ≤ 180)")
		return
	}
	if !utils.ValidCoords(req.EndLat, req.EndLng) {
		response.ValidationFail(c, "end coordinates out of valid range (-90 ≤ lat ≤ 90, -180 ≤ lng ≤ 180)")
		return
	}

	mode := req.Mode
	if req.VehicleType != "" {
		mode = req.VehicleType
	}
	if _, err := routing.NormalizeMode(mode); err != nil {
		response.ValidationFail(c, err.Error())
		return
	}

	start := time.Now()
	resp, err := h.svc.Calculate(c.Request.Context(), &req)
	elapsed := time.Since(start)

	if err != nil {
		if errors.Is(err, service.ErrRoutingOverloaded) {
			response.Fail(c, http.StatusServiceUnavailable, "ROUTING_OVERLOADED",
				"routing engine is busy; please retry in a moment")
			return
		}
		response.Fail(c, http.StatusInternalServerError, "ROUTING_ERROR", err.Error())
		return
	}

	middleware.RouteCalcTotal.Inc()
	middleware.RouteCalcLatency.Observe(elapsed.Seconds())

	response.OK(c, resp)
}
