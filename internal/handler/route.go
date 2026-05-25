package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"geo-service/internal/middleware"
	"geo-service/internal/model"
	"geo-service/internal/response"
	"geo-service/internal/routing"
	"geo-service/internal/service"
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
//	@Failure		404		{object}	response.Failure								"No route found"
//	@Failure		503		{object}	response.Failure								"Routing backend unavailable or overloaded"
//	@Failure		504		{object}	response.Failure								"Routing backend timeout"
//	@Failure		500		{object}	response.Failure								"Internal routing error"
//	@Router			/route [post]
func (h *RouteHandler) Calculate(c *gin.Context) {
	var req model.RouteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}

	if !validateCoords(c, req.StartLat, req.StartLng) {
		return
	}
	if !validateCoords(c, req.EndLat, req.EndLng) {
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
	resp, meta, err := h.svc.CalculateWithMeta(c.Request.Context(), &req)
	elapsed := time.Since(start)

	if err != nil {
		status, code, message := routeErrorResponse(err)
		if status >= 500 {
			slog.Error("route calculation failed", "err", err, "backend", meta.Backend, "mode", meta.Mode)
		}
		recordRouteErrorMetric(meta, err)
		logRouteRequest(meta, elapsed, status)
		response.Fail(c, status, code, message)
		return
	}

	middleware.RouteCalcTotal.Inc()
	middleware.RouteCalcLatency.Observe(elapsed.Seconds())
	logRouteRequest(meta, elapsed, http.StatusOK)

	response.OK(c, resp)
}

func routeErrorResponse(err error) (int, string, string) {
	return mapServiceError(err)
}

func recordRouteErrorMetric(meta service.RouteMeta, err error) {
	backend := meta.Backend
	if backend == "" {
		backend = "unknown"
	}
	if errors.Is(err, service.ErrRoutingTimeout) {
		middleware.RouteTimeoutTotal.WithLabelValues(backend).Inc()
	}
	if errors.Is(err, service.ErrRoutingOverloaded) {
		middleware.RouteOverloadTotal.WithLabelValues(backend).Inc()
	}
}

func logRouteRequest(meta service.RouteMeta, latency time.Duration, status int) {
	slog.Info("route request",
		"backend", meta.Backend,
		"cache_hit", meta.CacheHit,
		"mode", meta.Mode,
		"alternatives", meta.Alternatives,
		"latency_ms", latency.Milliseconds(),
		"status", status,
	)
}
