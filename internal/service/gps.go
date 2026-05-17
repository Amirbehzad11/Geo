package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/redis/go-redis/v9"

	"geo-service/internal/cache"
	"geo-service/internal/events"
	"geo-service/internal/model"
	"geo-service/internal/routing"
	"geo-service/internal/utils"
)

// ErrRateLimited is returned when a GPS update arrives too quickly.
var ErrRateLimited = errors.New("rate limited: too many updates for this trip")

const smoothingAlpha = 0.75 // EMA weight for current observation

// GPSService processes incoming GPS updates.
//
// Pipeline:
//  1. Redis rate-limit check (SET NX)
//  2. Load previous state from Redis
//  3. Apply EMA GPS smoothing
//  4. Compute instantaneous speed
//  5. Detect route deviation (if route is cached)
//  6. Persist new state + publish LocationUpdated event (goroutine)
type GPSService struct {
	redis       *cache.Redis
	bus         *events.Bus
	rateLimitMs int64
	deviationKm float64
}

func NewGPSService(r *cache.Redis, bus *events.Bus, rateLimitMs int64, deviationKm float64) *GPSService {
	return &GPSService{
		redis:       r,
		bus:         bus,
		rateLimitMs: rateLimitMs,
		deviationKm: deviationKm,
	}
}

// ProcessUpdate validates, rate-limits, and processes a GPS update.
// Returns the resulting LocationState on success.
func (s *GPSService) ProcessUpdate(ctx context.Context, update *model.GPSUpdate) (*model.LocationState, error) {
	// --- 1. Rate limit ---
	ok, err := s.redis.AcquireRateLimit(ctx, update.TripID, s.rateLimitMs)
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("rate limit check: %w", err)
	}
	if !ok {
		return nil, ErrRateLimited
	}

	// --- 2. Load previous state ---
	var prev model.LocationState
	hasPrev := s.redis.GetLocation(ctx, update.TripID, &prev) == nil

	// --- 3. Smooth GPS coordinates ---
	lat, lng := update.Lat, update.Lng
	if hasPrev {
		lat, lng = utils.SmoothGPS(lat, lng, prev.Lat, prev.Lng, smoothingAlpha)
	}

	// --- 4. Compute speed ---
	speed := 0.0
	if hasPrev && prev.Timestamp > 0 {
		dt := float64(update.Timestamp-prev.Timestamp) / 3600.0 // seconds to hours
		if dt > 0 {
			dist := utils.Haversine(prev.Lat, prev.Lng, lat, lng)
			speed = math.Round((dist/dt)*10) / 10
		}
	}

	// --- 5. Route deviation ---
	deviation := s.computeDeviation(ctx, update.TripID, lat, lng)

	state := &model.LocationState{
		TripID:      update.TripID,
		Lat:         lat,
		Lng:         lng,
		SpeedKmH:    speed,
		Timestamp:   update.Timestamp,
		DeviationKm: math.Round(deviation*1000) / 1000,
	}

	// --- 6. Persist + broadcast (fire and forget) ---
	go s.persistAndPublish(state, deviation)

	return state, nil
}

// GetLocation returns the latest known location for a trip from Redis.
func (s *GPSService) GetLocation(ctx context.Context, tripID int64) (*model.LocationState, error) {
	var state model.LocationState
	if err := s.redis.GetLocation(ctx, tripID, &state); err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("no location found for trip %d", tripID)
		}
		return nil, err
	}
	return &state, nil
}

// persistAndPublish stores state in Redis and publishes to the event bus.
func (s *GPSService) persistAndPublish(state *model.LocationState, deviation float64) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_ = s.redis.SetLocation(ctx, state.TripID, state)
	_ = s.redis.SetLastUpdate(ctx, state.TripID, state.Timestamp)

	if ev, err := events.New(events.LocationUpdated, state.TripID, state); err == nil {
		_ = s.bus.Publish(ctx, ev)
	}

	// Publish a separate deviation event when driver is significantly off-route.
	if deviation > s.deviationKm {
		if ev, err := events.New(events.DeviationDetected, state.TripID, state); err == nil {
			_ = s.bus.Publish(ctx, ev)
		}
	}
}

// computeDeviation returns the distance in km from the current position
// to the nearest point on the cached trip route. Returns 0 if no route is cached.
func (s *GPSService) computeDeviation(ctx context.Context, tripID int64, lat, lng float64) float64 {
	var routeResp model.RouteResponse
	if err := s.redis.GetTripRoute(ctx, tripID, &routeResp); err != nil {
		return 0
	}
	if routeResp.Polyline == "" {
		return 0
	}

	rawPts := routing.DecodePolyline(routeResp.Polyline)
	if len(rawPts) < 2 {
		return 0
	}

	pts := make([]utils.PolyPoint, len(rawPts))
	for i, p := range rawPts {
		pts[i] = utils.PolyPoint{Lat: p.Lat, Lng: p.Lng}
	}
	return utils.MinDistanceToPolyline(lat, lng, pts)
}
