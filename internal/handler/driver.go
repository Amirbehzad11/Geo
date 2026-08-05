package handler

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"geo-service/internal/middleware"
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

	driverID, ok := resolveAuthenticatedDriverID(c, req.DriverID.String())
	if !ok {
		return
	}
	req.DriverID = model.StringID(driverID)

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

// GoOffline removes the caller's live location right away, instead of
// waiting for driverLocationTTL to expire. Called when a passenger leaves
// the map (navigates away, hides the tab, or closes it) so senders stop
// seeing their stale position immediately.
func (h *DriverHandler) GoOffline(c *gin.Context) {
	if h.svc == nil {
		response.Fail(c, http.StatusServiceUnavailable, "DRIVER_LOCATION_DISABLED", "driver location service is not configured")
		return
	}

	var req model.DriverLocationRequest
	_ = c.ShouldBindJSON(&req)

	driverID, ok := resolveAuthenticatedDriverID(c, req.DriverID.String())
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), driverLocationTimeout)
	defer cancel()

	if err := h.svc.RemoveLocation(ctx, driverID); err != nil {
		status, code, message := mapServiceError(err)
		if status >= 500 {
			slog.Error("driver go-offline failed", "err", err, "driver_id", driverID)
		}
		response.Fail(c, status, code, message)
		return
	}

	response.OK(c, gin.H{"type": "driver.location.removed", "driver_id": driverID})
}

// resolveAuthenticatedDriverID picks the Redis member id for a location write.
// JWT clients: always use token user_id/sub (body driver_id is ignored).
// API-key clients (simulators): require explicit body driver_id.
func resolveAuthenticatedDriverID(c *gin.Context, bodyDriverID string) (string, bool) {
	if middleware.AuthenticatedWithAPIKey(c) {
		id := strings.TrimSpace(bodyDriverID)
		if id == "" {
			response.Fail(c, http.StatusBadRequest, "DRIVER_ID_REQUIRED", "driver_id is required for API-key clients")
			return "", false
		}
		return id, true
	}

	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "authenticated user is required")
		return "", false
	}
	return strconv.FormatInt(userID, 10), true
}
