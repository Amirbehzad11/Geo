package gps

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
	if errors.Is(err, ErrRateLimited) {
		return http.StatusTooManyRequests, "RATE_LIMITED", "update received too soon; please slow down"
	}
	return http.StatusInternalServerError, "INTERNAL_ERROR", "internal error; please retry"
}
