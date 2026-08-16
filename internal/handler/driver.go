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
	svc     *service.DriverService
	userACL UserLocationAuthorizer
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

// UserLocationAuthorizer decides whether the caller may read another user's
// live position.
type UserLocationAuthorizer interface {
	UsersShareActiveShipping(ctx context.Context, viewerID, targetID int64) (bool, error)
}

// WithUserLocationACL enables GET /gps/user/:id/location for counterparts of
// the target user. Without it the endpoint only serves the caller's own position.
func (h *DriverHandler) WithUserLocationACL(authz UserLocationAuthorizer) *DriverHandler {
	h.userACL = authz
	return h
}

// GetUserLocation handles GET /gps/user/:id/location
//
//	@Summary		Get a user's live location
//	@Description	Returns the position the user last reported (POST /gps/update without trip_id, or POST /driver-location), read from Redis. Positions live for 2 minutes after the last update. Readable by the user themselves and by counterparts on a shared shipping.
//	@Tags			gps
//	@Produce		json
//	@Param			id	path		int										true	"User ID"
//	@Success		200	{object}	response.Success{data=model.DriverLocation}	"Latest user location"
//	@Failure		400	{object}	response.Failure						"Invalid user ID"
//	@Failure		401	{object}	response.Failure						"Authentication required"
//	@Failure		403	{object}	response.Failure						"Not allowed to read this user's location"
//	@Failure		404	{object}	response.Failure						"No live location for this user"
//	@Router			/gps/user/{id}/location [get]
func (h *DriverHandler) GetUserLocation(c *gin.Context) {
	if h.svc == nil {
		response.Fail(c, http.StatusServiceUnavailable, "DRIVER_LOCATION_DISABLED", "driver location service is not configured")
		return
	}

	targetID, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || targetID <= 0 {
		response.Fail(c, http.StatusBadRequest, "INVALID_USER_ID", "user id must be a positive integer")
		return
	}

	if !h.authorizeUserLocationRead(c, targetID) {
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), driverLocationTimeout)
	defer cancel()

	location, err := h.svc.GetPresence(ctx, strconv.FormatInt(targetID, 10))
	if err != nil {
		status, code, message := mapServiceError(err)
		if status >= 500 {
			slog.Error("user location read failed", "err", err, "user_id", targetID)
		}
		response.Fail(c, status, code, message)
		return
	}

	response.OK(c, location)
}

// authorizeUserLocationRead lets API-key clients (simulators, backend jobs)
// through, and limits JWT clients to their own position or that of a
// counterpart on a shared shipping.
func (h *DriverHandler) authorizeUserLocationRead(c *gin.Context, targetID int64) bool {
	if middleware.AuthenticatedWithAPIKey(c) {
		return true
	}

	viewerID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "authenticated user is required")
		return false
	}
	if viewerID == targetID {
		return true
	}
	if h.userACL == nil {
		response.Fail(c, http.StatusServiceUnavailable, "AUTHORIZATION_UNAVAILABLE", "user location authorization is not configured")
		return false
	}

	allowed, err := h.userACL.UsersShareActiveShipping(c.Request.Context(), viewerID, targetID)
	if err != nil {
		slog.Error("user location authorization failed", "err", err, "viewer_id", viewerID, "user_id", targetID)
		response.Fail(c, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error; please retry")
		return false
	}
	if !allowed {
		response.Fail(c, http.StatusForbidden, "FORBIDDEN", "user location access denied")
		return false
	}

	return true
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
