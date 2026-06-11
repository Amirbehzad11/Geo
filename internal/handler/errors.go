package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"geo-service/internal/response"
	"geo-service/internal/service"
	"geo-service/internal/utils"
)

// validateCoords returns false (and writes the validation error response) when
// (lat, lng) is outside the WGS-84 range. Callers must return immediately when
// it returns false.
func validateCoords(c *gin.Context, lat, lng float64) bool {
	if !utils.ValidCoords(lat, lng) {
		response.ValidationFail(c, "coordinates out of valid range (-90 ≤ lat ≤ 90, -180 ≤ lng ≤ 180)")
		return false
	}
	return true
}

// mapServiceError converts a service-layer error into an HTTP status, error
// code, and public-facing message. It NEVER leaks raw err.Error() for 5xx
// responses; callers are expected to log the underlying error themselves.
func mapServiceError(err error) (status int, code string, message string) {
	switch {
	case errors.Is(err, service.ErrDriverIDRequired):
		return http.StatusUnprocessableEntity, "VALIDATION_ERROR", err.Error()
	case errors.Is(err, service.ErrDriverLocationDisabled):
		return http.StatusServiceUnavailable, "DRIVER_LOCATION_DISABLED", "driver location service is not configured"
	default:
		return http.StatusInternalServerError, "INTERNAL_ERROR", "internal error; please retry"
	}
}
