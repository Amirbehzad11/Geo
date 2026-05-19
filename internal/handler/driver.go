package handler

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"geo-service/internal/model"
	"geo-service/internal/response"
	"geo-service/internal/service"
	"geo-service/internal/utils"
)

const driverLocationTimeout = 3 * time.Second

type DriverHandler struct {
	svc *service.DriverService
}

func NewDriverHandler(svc *service.DriverService) *DriverHandler {
	return &DriverHandler{svc: svc}
}

func (h *DriverHandler) UpdateLocation(c *gin.Context) {
	if h.svc == nil {
		response.Fail(c, http.StatusServiceUnavailable, "DRIVER_LOCATION_DISABLED", "driver location service is not configured")
		return
	}

	var req model.DriverLocationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if !utils.ValidCoords(req.Lat, req.Lng) {
		response.ValidationFail(c, "coordinates out of valid range (-90 <= lat <= 90, -180 <= lng <= 180)")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), driverLocationTimeout)
	defer cancel()

	result, err := h.svc.UpdateLocation(ctx, req)
	if err != nil {
		if errors.Is(err, service.ErrDriverIDRequired) {
			response.ValidationFail(c, err.Error())
			return
		}
		if errors.Is(err, service.ErrDriverLocationDisabled) {
			response.Fail(c, http.StatusServiceUnavailable, "DRIVER_LOCATION_DISABLED", err.Error())
			return
		}
		response.Fail(c, http.StatusInternalServerError, "DRIVER_LOCATION_FAILED", err.Error())
		return
	}

	response.OK(c, result)
}
