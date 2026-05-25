package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"geo-service/internal/model"
	"geo-service/internal/response"
	"geo-service/internal/service"
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
	if !validateCoords(c, req.Lat, req.Lng) {
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), driverLocationTimeout)
	defer cancel()

	result, err := h.svc.UpdateLocation(ctx, req)
	if err != nil {
		status, code, message := mapServiceError(err)
		if status >= 500 {
			slog.Error("driver location update failed", "err", err, "driver_id", req.DriverID)
		}
		response.Fail(c, status, code, message)
		return
	}

	response.OK(c, result)
}
