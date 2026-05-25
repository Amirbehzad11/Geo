package service

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sync/singleflight"

	"geo-service/internal/cache"
	"geo-service/internal/events"
	"geo-service/internal/middleware"
	"geo-service/internal/model"
	"geo-service/internal/routing"
	"geo-service/internal/storage"
)

const (
	defaultRouteAlternatives = 1
	maxRouteAlternatives     = 3
)

type RouteMeta struct {
	Backend      string
	CacheHit     bool
	Mode         string
	Alternatives int
}

type routeComputeValue struct {
	resp    *model.RouteResponse
	backend string
}

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

	RouteCachePrecision int
	MaxAlternatives     int
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
	backend         RouteBackend
	redis           *cache.Redis
	bus             *events.Bus
	pg              *storage.Postgres
	group           singleflight.Group
	sem             chan struct{} // nil means unlimited
	semTimeout      time.Duration
	routeTimeout    time.Duration
	cachePrecision  int
	maxAlternatives int
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

	if cfg.RouteCachePrecision <= 0 {
		svc.cachePrecision = 5
	} else {
		svc.cachePrecision = cache.NormalizeRoutePrecision(cfg.RouteCachePrecision)
	}
	svc.maxAlternatives = cfg.MaxAlternatives
	if svc.maxAlternatives <= 0 {
		svc.maxAlternatives = defaultRouteAlternatives
	}
	if svc.maxAlternatives > maxRouteAlternatives {
		svc.maxAlternatives = maxRouteAlternatives
	}

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
	resp, _, err := s.CalculateWithMeta(ctx, req)
	return resp, err
}

func (s *RouteService) CalculateWithMeta(ctx context.Context, req *model.RouteRequest) (*model.RouteResponse, RouteMeta, error) {
	meta := RouteMeta{Backend: s.backend.BackendName()}

	mode, err := routing.NormalizeMode(routeMode(req))
	if err != nil {
		return nil, meta, err
	}
	meta.Mode = string(mode)

	alternatives := normalizeAlternatives(req.Alternatives, s.maxAlternatives)
	meta.Alternatives = alternatives
	key := cache.RouteKeyWithPrecision(s.backend.BackendName(), string(mode), alternatives, s.cachePrecision,
		req.StartLat, req.StartLng, req.EndLat, req.EndLng)

	var cached model.RouteResponse
	if s.redis != nil {
		if err := s.redis.GetRoute(ctx, key, &cached); err == nil && cached.Distance > 0 {
			middleware.RouteCacheTotal.WithLabelValues("hit").Inc()
			middleware.RouteBackendTotal.WithLabelValues("cache", "success").Inc()
			meta.CacheHit = true
			meta.Backend = "cache"
			if req.TripID > 0 {
				_ = s.redis.SetTripRoute(ctx, req.TripID, &cached)
			}
			s.persistRoute(req, &cached)
			return &cached, meta, nil
		}
		middleware.RouteCacheTotal.WithLabelValues("miss").Inc()
	}

	if s.sem != nil {
		select {
		case s.sem <- struct{}{}:
			defer func() { <-s.sem }()
		default:
			timer := time.NewTimer(s.semTimeout)
			defer timer.Stop()
			select {
			case s.sem <- struct{}{}:
				defer func() { <-s.sem }()
			case <-timer.C:
				middleware.RouteOverloadTotal.WithLabelValues("total").Inc()
				return nil, meta, ErrRoutingOverloaded
			case <-ctx.Done():
				return nil, meta, fmt.Errorf("%w: request cancelled while waiting for total routing slot: %v", ErrRoutingTimeout, ctx.Err())
			}
		}
	}

	v, computeErr, _ := s.group.Do(key, func() (any, error) {
		computeCtx := context.Background()
		if s.routeTimeout > 0 {
			var cancel context.CancelFunc
			computeCtx, cancel = context.WithTimeout(computeCtx, s.routeTimeout)
			defer cancel()
		}

		result, err := computeRouteResult(
			s.backend, computeCtx, mode, alternatives,
			req.StartLat, req.StartLng, req.EndLat, req.EndLng,
		)
		if err != nil {
			return nil, err
		}

		if s.redis != nil {
			cacheCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = s.redis.SetRoute(cacheCtx, key, result.Response)
		}

		return routeComputeValue{resp: result.Response, backend: result.Backend}, nil
	})
	if computeErr != nil {
		return nil, meta, computeErr
	}

	result := v.(routeComputeValue)
	resp := result.resp
	if result.backend != "" {
		meta.Backend = result.backend
	}

	if s.redis != nil && req.TripID > 0 {
		_ = s.redis.SetTripRoute(ctx, req.TripID, resp)
	}
	s.persistRoute(req, resp)
	s.publishRouteCalculated(req.TripID, resp)

	return resp, meta, nil
}

// ---- helpers ----------------------------------------------------------------

func routeMode(req *model.RouteRequest) string {
	if req.VehicleType != "" {
		return req.VehicleType
	}
	return req.Mode
}

func normalizeAlternatives(n int, maxAllowed ...int) int {
	max := maxRouteAlternatives
	if len(maxAllowed) > 0 && maxAllowed[0] > 0 {
		max = maxAllowed[0]
	}
	if max > maxRouteAlternatives {
		max = maxRouteAlternatives
	}
	if n <= 0 {
		return defaultRouteAlternatives
	}
	if n > max {
		return max
	}
	return n
}

// buildMultiModalTrainResponse converts walk→train→walk legs into a RouteResponse.
func buildMultiModalTrainResponse(legs []*routing.MultiModalLeg) *model.RouteResponse {
	return buildMultiModalResponse(routing.ModeTrain, legs)
}

// buildMultiModalTransitResponse converts a walk → bus/metro → walk public-transport
// itinerary into a RouteResponse.
func buildMultiModalTransitResponse(legs []*routing.MultiModalLeg) *model.RouteResponse {
	return buildMultiModalResponse(routing.ModePublicTransport, legs)
}

// buildMultiModalResponse is the shared implementation used by every multi-modal
// route mode. It aggregates per-leg distance/duration and stitches polylines so
// clients that ignore .Legs still get a usable end-to-end route.
func buildMultiModalResponse(topMode routing.TransportMode, legs []*routing.MultiModalLeg) *model.RouteResponse {
	modelLegs := make([]model.RouteLeg, 0, len(legs))
	var totalDist, totalDur float64
	var allPoints []routing.Point

	for _, leg := range legs {
		if leg.Route == nil {
			continue
		}
		modelLegs = append(modelLegs, model.RouteLeg{
			Mode:         string(leg.Mode),
			DistanceKm:   leg.Route.Distance,
			DurationMin:  leg.Route.Duration,
			Polyline:     leg.Route.Polyline,
			Instructions: routeInstructions(leg.Route.Instructions),
		})
		totalDist += leg.Route.Distance
		totalDur += leg.Route.Duration
		allPoints = append(allPoints, routing.DecodePolyline(leg.Route.Polyline)...)
	}

	combinedPolyline := routing.EncodePolyline(allPoints)
	modeStr := string(topMode)

	return &model.RouteResponse{
		Mode:     modeStr,
		Distance: totalDist,
		Duration: totalDur,
		Polyline: combinedPolyline,
		Legs:     modelLegs,
		Primary: model.RouteOption{
			ID:          1,
			Mode:        modeStr,
			IsPrimary:   true,
			DistanceKm:  totalDist,
			DurationMin: totalDur,
			Polyline:    combinedPolyline,
		},
	}
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
	if tripID <= 0 || s.bus == nil {
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
