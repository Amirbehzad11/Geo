package service

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"geo-service/internal/cache"
	"geo-service/internal/model"
)

var (
	ErrDriverLocationDisabled = errors.New("driver location redis is not configured")
	ErrDriverIDRequired       = errors.New("driver_id is required")
	ErrDriverLocationNotFound = errors.New("no live location for this user")
)

const defaultDriverSearchRadiusKm = 20.0

// DriverShippingRepository loads nearby-sender enrichments from Laravel DB.
type DriverShippingRepository interface {
	FindLatestActiveShippingDestinationsByUserIDs(ctx context.Context, userIDs []int64) (map[int64]model.DriverActiveJob, error)
	FindLatestTripDestinationsByUserIDs(ctx context.Context, userIDs []int64) (map[int64]model.DriverLatestTrip, error)
	FindVisibleOnMapUserIDs(ctx context.Context, userIDs []int64) (map[int64]struct{}, error)
}

type DriverService struct {
	redis           *cache.Redis
	shippings       DriverShippingRepository
	geoKey          string
	streamKey       string
	defaultRadiusKm float64
	defaultLimit    int
}

func NewDriverService(redis *cache.Redis, geoKey, streamKey string, defaultRadiusKm float64, defaultLimit int, shippings DriverShippingRepository) *DriverService {
	if defaultRadiusKm <= 0 {
		defaultRadiusKm = defaultDriverSearchRadiusKm
	}
	if defaultLimit <= 0 {
		defaultLimit = defaultShipmentLimit
	}
	if defaultLimit > maxShipmentLimit {
		defaultLimit = maxShipmentLimit
	}

	return &DriverService{
		redis:           redis,
		shippings:       shippings,
		geoKey:          strings.TrimSpace(geoKey),
		streamKey:       strings.TrimSpace(streamKey),
		defaultRadiusKm: defaultRadiusKm,
		defaultLimit:    defaultLimit,
	}
}

func (s *DriverService) UpdateLocation(ctx context.Context, req model.DriverLocationRequest) (*model.DriverLocationResponse, error) {
	if s == nil || s.redis == nil || s.geoKey == "" {
		return nil, ErrDriverLocationDisabled
	}

	driverID := strings.TrimSpace(req.DriverID.String())
	if driverID == "" {
		return nil, ErrDriverIDRequired
	}

	now := time.Now().UnixMilli()
	timestampMs := req.TimestampMs
	if timestampMs <= 0 {
		timestampMs = now
	}

	state := cache.DriverLocationState{
		ID:          driverID,
		Lat:         req.Lat,
		Lng:         req.Lng,
		TimestampMs: timestampMs,
	}
	if err := s.redis.SetDriverLocation(ctx, s.geoKey, s.streamKey, state); err != nil {
		return nil, err
	}

	return &model.DriverLocationResponse{
		Type:      "driver.location.updated",
		Timestamp: now,
		Driver: model.DriverLocation{
			ID:          state.ID,
			Lat:         state.Lat,
			Lng:         state.Lng,
			TimestampMs: state.TimestampMs,
		},
	}, nil
}

// RemoveLocation drops a driver from the geo index and location hash right
// away, instead of waiting for the location hash TTL to expire. Called when
// a passenger explicitly leaves the map.
func (s *DriverService) RemoveLocation(ctx context.Context, driverID string) error {
	if s == nil || s.redis == nil || s.geoKey == "" {
		return ErrDriverLocationDisabled
	}

	driverID = strings.TrimSpace(driverID)
	if driverID == "" {
		return ErrDriverIDRequired
	}

	return s.redis.RemoveDriverLocation(ctx, s.geoKey, driverID)
}

// UpdatePresence stores a user's live map position (used by POST /gps/update without trip_id).
func (s *DriverService) UpdatePresence(ctx context.Context, userID string, lat, lng float64, timestampMs int64) (any, error) {
	return s.UpdateLocation(ctx, model.DriverLocationRequest{
		DriverID:    model.StringID(userID),
		Lat:         lat,
		Lng:         lng,
		TimestampMs: timestampMs,
	})
}

// GetPresence returns the live position a single user last reported, read from
// the same Redis entry that POST /gps/update and POST /driver-location write.
// Positions are tracked per user, not per trip, so this is what a sender
// follows while their package is on the road.
func (s *DriverService) GetPresence(ctx context.Context, userID string) (*model.DriverLocation, error) {
	if s == nil || s.redis == nil {
		return nil, ErrDriverLocationDisabled
	}

	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrDriverIDRequired
	}

	state, ok := s.redis.GetDriverLocation(ctx, userID)
	if !ok {
		return nil, ErrDriverLocationNotFound
	}

	location := &model.DriverLocation{
		ID:          state.ID,
		Lat:         state.Lat,
		Lng:         state.Lng,
		TimestampMs: state.TimestampMs,
	}
	if id, parsed := parseDriverUserID(state.ID); parsed {
		location.DriverID = id
	}

	return location, nil
}

// SearchNearby returns live passengers near lat/lng for the sender map.
// excludeUserID (when > 0) hides the requesting sender from their own results.
func (s *DriverService) SearchNearby(ctx context.Context, lat, lng, radiusKm float64, limit int, excludeUserID int64) (*model.NearbyDriverResponse, error) {
	if s == nil || s.redis == nil || s.geoKey == "" {
		return nil, ErrDriverLocationDisabled
	}
	if radiusKm <= 0 {
		radiusKm = s.defaultRadiusKm
	}
	if radiusKm > maxNearbySearchRadiusKm {
		radiusKm = maxNearbySearchRadiusKm
	}
	if limit <= 0 {
		limit = s.defaultLimit
	}
	if limit > maxShipmentLimit {
		limit = maxShipmentLimit
	}

	// Over-fetch so visible_on_map / self filtering still has enough candidates.
	candidateLimit := limit * 3
	if candidateLimit > maxShipmentLimit {
		candidateLimit = maxShipmentLimit
	}

	states, err := s.redis.FindNearbyDrivers(ctx, s.geoKey, lat, lng, radiusKm, candidateLimit)
	if err != nil {
		return nil, err
	}

	drivers := make([]model.DriverLocation, 0, len(states))
	for _, state := range states {
		driverID, ok := parseDriverUserID(state.ID)
		if ok && excludeUserID > 0 && driverID == excludeUserID {
			continue
		}
		driver := model.DriverLocation{
			ID:          state.ID,
			Lat:         state.Lat,
			Lng:         state.Lng,
			TimestampMs: state.TimestampMs,
			DistanceKm:  state.DistanceKm,
			Active:      nil,
		}
		if ok {
			driver.DriverID = driverID
		}
		drivers = append(drivers, driver)
	}
	drivers = s.filterVisibleOnMap(ctx, drivers, limit)
	s.attachActiveShippings(ctx, drivers)

	return &model.NearbyDriverResponse{
		Type:      "driver.nearby",
		Timestamp: time.Now().UnixMilli(),
		Query: model.NearbyDriverQuery{
			Lat:      lat,
			Lng:      lng,
			RadiusKm: radiusKm,
			Limit:    limit,
		},
		Count:   len(drivers),
		Drivers: drivers,
	}, nil
}

// filterVisibleOnMap keeps only drivers whose profiles.visible_on_map is true.
// When the profile repository is unavailable, returns an empty list (fail closed).
func (s *DriverService) filterVisibleOnMap(ctx context.Context, drivers []model.DriverLocation, limit int) []model.DriverLocation {
	if len(drivers) == 0 {
		return drivers
	}
	if s == nil || s.shippings == nil {
		slog.Warn("nearby driver visible_on_map filter skipped — profile repository unavailable")
		return drivers[:0]
	}

	seen := make(map[int64]struct{}, len(drivers))
	userIDs := make([]int64, 0, len(drivers))
	for i := range drivers {
		if drivers[i].DriverID <= 0 {
			continue
		}
		if _, ok := seen[drivers[i].DriverID]; ok {
			continue
		}
		seen[drivers[i].DriverID] = struct{}{}
		userIDs = append(userIDs, drivers[i].DriverID)
	}
	if len(userIDs) == 0 {
		return drivers[:0]
	}

	visible, err := s.shippings.FindVisibleOnMapUserIDs(ctx, userIDs)
	if err != nil {
		slog.Warn("nearby driver visible_on_map filter failed", "err", err)
		return drivers[:0]
	}

	out := make([]model.DriverLocation, 0, min(len(drivers), limit))
	for i := range drivers {
		if drivers[i].DriverID <= 0 {
			continue
		}
		if _, ok := visible[drivers[i].DriverID]; !ok {
			continue
		}
		out = append(out, drivers[i])
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func (s *DriverService) attachActiveShippings(ctx context.Context, drivers []model.DriverLocation) {
	if s == nil || s.shippings == nil || len(drivers) == 0 {
		return
	}

	seen := make(map[int64]struct{}, len(drivers))
	userIDs := make([]int64, 0, len(drivers))
	for i := range drivers {
		if drivers[i].DriverID <= 0 {
			continue
		}
		if _, ok := seen[drivers[i].DriverID]; ok {
			continue
		}
		seen[drivers[i].DriverID] = struct{}{}
		userIDs = append(userIDs, drivers[i].DriverID)
	}
	if len(userIDs) == 0 {
		return
	}

	// Step 1: fetch active shippings for all drivers.
	byUser, err := s.shippings.FindLatestActiveShippingDestinationsByUserIDs(ctx, userIDs)
	if err != nil {
		slog.Warn("nearby driver shipping enrichment failed", "err", err)
		return
	}

	// Step 2: find drivers that have NO active shipping so we can fall back to
	// their latest trip destination.
	noShippingIDs := make([]int64, 0, len(userIDs))
	for _, id := range userIDs {
		if _, has := byUser[id]; !has {
			noShippingIDs = append(noShippingIDs, id)
		}
	}

	var latestTrips map[int64]model.DriverLatestTrip
	if len(noShippingIDs) > 0 {
		latestTrips, err = s.shippings.FindLatestTripDestinationsByUserIDs(ctx, noShippingIDs)
		if err != nil {
			slog.Warn("nearby driver latest trip enrichment failed", "err", err)
			// non-fatal: proceed without trip fallback
			latestTrips = nil
		}
	}

	// Step 3: attach to each driver — shipping takes priority, trip is fallback.
	for i := range drivers {
		if drivers[i].DriverID <= 0 {
			continue
		}
		id := drivers[i].DriverID
		if job, ok := byUser[id]; ok {
			active := job
			active.HasShipping = true
			drivers[i].Active = &active
		} else if latestTrips != nil {
			if trip, ok := latestTrips[id]; ok {
				drivers[i].Active = &model.DriverActiveJob{
					HasShipping: false,
					// Backward compatibility: some UIs still read active.destination /
					// active.trip_id for trip fallback.
					Destination: trip.Destination,
					TripID:      trip.TripID,
					// VehicleTypeID/Image are also needed here: the sender-map "send
					// request to passenger" flow reads active.vehicle_type_id to price
					// the shipment, and most passengers only have a registered trip
					// (no active shipping yet) when a sender first finds them.
					VehicleTypeID:    trip.VehicleTypeID,
					VehicleTypeImage: trip.VehicleTypeImage,
					LatestTrip:       &trip,
				}
			}
		}
	}
}

func parseDriverUserID(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
