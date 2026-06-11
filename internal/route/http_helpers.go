package route

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"geo-service/internal/response"
	"geo-service/internal/utils"
)

func validateCoords(c *gin.Context, lat, lng float64) bool {
	if !utils.ValidCoords(lat, lng) {
		response.ValidationFail(c, "coordinates out of valid range (-90 <= lat <= 90, -180 <= lng <= 180)")
		return false
	}
	return true
}

func mapServiceError(err error) (status int, code string, message string) {
	switch {
	case errors.Is(err, ErrRoutingOverloaded):
		return http.StatusServiceUnavailable, "ROUTING_OVERLOADED", "routing engine is busy; please retry in a moment"
	case errors.Is(err, ErrRoutingTimeout):
		return http.StatusGatewayTimeout, "ROUTING_TIMEOUT", "routing backend timed out; please retry in a moment"
	case errors.Is(err, ErrRouteNotFound):
		return http.StatusNotFound, "ROUTE_NOT_FOUND", "no route found between the given coordinates"
	case errors.Is(err, ErrRoutingBackendUnavailable):
		return http.StatusServiceUnavailable, "ROUTING_BACKEND_UNAVAILABLE", "routing backend is unavailable"
	default:
		return http.StatusInternalServerError, "INTERNAL_ERROR", "internal error; please retry"
	}
}
