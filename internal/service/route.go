package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sync/singleflight"

	"geo-service/internal/cache"
	"geo-service/internal/events"
	"geo-service/internal/model"
	"geo-service/internal/routing"
	"geo-service/internal/storage"
)

// ErrRoutingOverloaded is returned by Calculate when the in-flight semaphore is
// full and the queue-wait timeout expires before a slot becomes free.
var ErrRoutingOverloaded = errors.New("routing overloaded: too many concurrent requests")

const (
	defaultRouteAlternatives = 1
	maxRouteAlternatives     = 3
)

// RouteServiceConfig carries tunables injected at construction time.
type RouteServiceConfig struct {
	// MaxInFlight is the maximum number of concurrent routing computations.
	// Zero (default) means unlimited.
	MaxInFlight int

	// QueueTimeoutMs is how long (in ms) Calculate waits for a semaphore slot
	// before returning ErrRoutingOverloaded. Defaults to 1 000 ms.
	QueueTimeoutMs int64

	// RouteTimeoutMs is the maximum wall-clock time allowed for a single
	// backend.ComputeRoute call. Zero means no explicit timeout.
	RouteTimeoutMs int64
}

// RouteService computes routes, caches results, persists them, and fires events.
//
// Concurrency model
//
//	┌──────────┐     cache hit          ┌──────────────┐
//	│ incoming │──────────────────────▶│  return early │  (no semaphore consumed)
//	│ request  │                        └──────────────┘
//	└─────┬────┘
//	      │ cache miss
//	      ▼
//	┌──────────────────────────────────┐
//	│  semaphore (MaxInFlight slots)   │  503 if full & queue-timeout expires
//	└──────────────┬───────────────────┘
//	               │ slot acquired
//	               ▼
//	┌──────────────────────────────────┐
//	│  singleflight.Group              │  dedup identical concurrent misses —
//	│  key = cache key                 │  only one backend call fires per key
//	└──────────────┬───────────────────┘
//	               │ result
//	               ▼
//	         persist + publish
type RouteService struct {
	backend      RouteBackend
	redis        *cache.Redis
	bus          *events.Bus
	pg           *storage.Postgres
	group        singleflight.Group
	sem          chan struct{} // nil means unlimited
	semTimeout   time.Duration
	routeTimeout time.Duration
}

// NewRouteService constructs a RouteService with the given backend and config.
func NewRouteService(
	backend RouteBackend,
	redis *cache.Redis,
	bus *events.Bus,
	pg *storage.Postgres,
	cfg RouteServiceConfig,
) *RouteService {
	svc := &RouteService{
		backend: backend,
		redis:   redis,
		bus:     bus,
		pg:      pg,
	}

	if cfg.MaxInFlight > 0 {
		svc.sem = make(chan struct{}, cfg.MaxInFlight)
	}

	svc.semTimeout = time.Duration(cfg.QueueTimeoutMs) * time.Millisecond
	if svc.semTimeout <= 0 {
		svc.semTimeout = time.Second
	}

	svc.routeTimeout = time.Duration(cfg.RouteTimeoutMs) * time.Millisecond
	// routeTimeout == 0 → no explicit timeout on backend calls

	return svc
}

// Calculate returns suggested routes for the given request.
//
// Sequence:
//  1. Validate mode and normalise alternatives.
//  2. Check Redis cache — return immediately on hit (no semaphore consumed).
//  3. Acquire in-flight semaphore slot (if MaxInFlight > 0).
//  4. Use singleflight to deduplicate concurrent identical cache-miss calls.
//  5. Store result in Redis, persist async, publish event.
func (s *RouteService) Calculate(ctx context.Context, req *model.RouteRequest) (*model.RouteResponse, error) {
	mode, err := routing.NormalizeMode(routeMode(req))
	if err != nil {
		return nil, err
	}

	alternatives := normalizeAlternatives(req.Alternatives)
	key := cache.RouteKey(s.backend.BackendName(), string(mode), alternatives,
		req.StartLat, req.StartLng, req.EndLat, req.EndLng)

	// ── 1. Cache hit — serve without touching the semaphore ──────────────────
	var cached model.RouteResponse
	if err := s.redis.GetRoute(ctx, key, &cached); err == nil && cached.Distance > 0 {
		if req.TripID > 0 {
			_ = s.redis.SetTripRoute(ctx, req.TripID, &cached)
		}
		s.persistRoute(req, &cached)
		return &cached, nil
	}

	// ── 2. Acquire in-flight semaphore slot ───────────────────────────────────
	if s.sem != nil {
		select {
		case s.sem <- struct{}{}:
			defer func() { <-s.sem }()
		default:
			// No free slot — wait up to semTimeout before rejecting.
			timer := time.NewTimer(s.semTimeout)
			defer timer.Stop()
			select {
			case s.sem <- struct{}{}:
				defer func() { <-s.sem }()
			case <-timer.C:
				return nil, ErrRoutingOverloaded
			case <-ctx.Done():
				return nil, fmt.Errorf("request cancelled while waiting for routing slot: %w", ctx.Err())
			}
		}
	}

	// ── 3. Singleflight: one backend call per unique route key ────────────────
	v, computeErr, _ := s.group.Do(key, func() (any, error) {
		// Use a background context with the configured timeout so that a single
		// caller's cancellation does not abort the shared computation (and any
		// co-waiters would lose the result too).
		computeCtx := context.Background()
		if s.routeTimeout > 0 {
			var cancel context.CancelFunc
			computeCtx, cancel = context.WithTimeout(computeCtx, s.routeTimeout)
			defer cancel()
		}

		resp, err := s.backend.ComputeRoute(
			computeCtx, mode, alternatives,
			req.StartLat, req.StartLng, req.EndLat, req.EndLng,
		)
		if err != nil {
			return nil, err
		}

		// Cache the result. Use a short-lived context independent of the caller.
		cacheCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.redis.SetRoute(cacheCtx, key, resp)

		return resp, nil
	})
	if computeErr != nil {
		return nil, computeErr
	}

	resp := v.(*model.RouteResponse)

	if req.TripID > 0 {
		_ = s.redis.SetTripRoute(ctx, req.TripID, resp)
	}
	s.persistRoute(req, resp)
	s.publishRouteCalculated(req.TripID, resp)

	return resp, nil
}

// ---- helpers ----------------------------------------------------------------

func routeMode(req *model.RouteRequest) string {
	if req.VehicleType != "" {
		return req.VehicleType
	}
	return req.Mode
}

func normalizeAlternatives(n int) int {
	if n <= 0 {
		return defaultRouteAlternatives
	}
	if n > maxRouteAlternatives {
		return maxRouteAlternatives
	}
	return n
}

// buildRouteResponse converts a slice of internal routing.Route values to the
// API model. It is also called by InternalBackend.ComputeRoute.
func buildRouteResponse(mode routing.TransportMode, routes []*routing.Route) *model.RouteResponse {
	opts := make([]model.RouteOption, 0, len(routes))
	for i, r := range routes {
		opts = append(opts, model.RouteOption{
			ID:           i + 1,
			Mode:         string(mode),
			IsPrimary:    i == 0,
			DistanceKm:   r.Distance,
			DurationMin:  r.Duration,
			Polyline:     r.Polyline,
			Instructions: routeInstructions(r.Instructions),
		})
	}

	resp := &model.RouteResponse{Mode: string(mode), Routes: opts}
	if len(opts) > 0 {
		resp.Primary = opts[0]
		resp.Distance = opts[0].DistanceKm
		resp.Duration = opts[0].DurationMin
		resp.Polyline = opts[0].Polyline
	}
	return resp
}

func routeInstructions(in []routing.Instruction) []model.RouteInstruction {
	if len(in) == 0 {
		return nil
	}
	out := make([]model.RouteInstruction, 0, len(in))
	for _, inst := range in {
		out = append(out, model.RouteInstruction{
			Index:       inst.Index,
			Type:        inst.Type,
			Modifier:    inst.Modifier,
			Text:        inst.Text,
			DistanceKm:  inst.DistanceKm,
			DurationMin: inst.DurationMin,
			Location:    model.RoutePoint{Lat: inst.Location.Lat, Lng: inst.Location.Lng},
			StreetName:  inst.StreetName,
		})
	}
	return out
}

func (s *RouteService) persistRoute(req *model.RouteRequest, resp *model.RouteResponse) {
	if s.pg == nil {
		return
	}
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.pg.SaveRouteCalculation(bgCtx, req, resp)
	}()
}

func (s *RouteService) publishRouteCalculated(tripID int64, resp *model.RouteResponse) {
	if tripID <= 0 {
		return
	}
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if ev, err := events.New(events.RouteCalculated, tripID, resp); err == nil {
			_ = s.bus.Publish(bgCtx, ev)
		}
	}()
}
