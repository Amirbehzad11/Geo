package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"geo-service/internal/cache"
	"geo-service/internal/model"
)

var (
	ErrDriverLocationDisabled = errors.New("driver location redis is not configured")
	ErrDriverIDRequired       = errors.New("driver_id is required")
)

const defaultDriverSearchRadiusKm = 20.0

type DriverService struct {
	redis           *cache.Redis
	geoKey          string
	streamKey       string
	defaultRadiusKm float64
	defaultLimit    int
}

func NewDriverService(redis *cache.Redis, geoKey, streamKey string, defaultRadiusKm float64, defaultLimit int) *DriverService {
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

func (s *DriverService) SearchNearby(ctx context.Context, lat, lng, radiusKm float64, limit int) (*model.NearbyDriverResponse, error) {
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

	states, err := s.redis.FindNearbyDrivers(ctx, s.geoKey, lat, lng, radiusKm, limit)
	if err != nil {
		return nil, err
	}

	drivers := make([]model.DriverLocation, 0, len(states))
	for _, state := range states {
		drivers = append(drivers, model.DriverLocation{
			ID:          state.ID,
			Lat:         state.Lat,
			Lng:         state.Lng,
			TimestampMs: state.TimestampMs,
			DistanceKm:  state.DistanceKm,
		})
	}

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
