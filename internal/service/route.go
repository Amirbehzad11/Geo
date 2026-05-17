package service

import (
	"context"
	"time"

	"geo-service/internal/cache"
	"geo-service/internal/events"
	"geo-service/internal/model"
	"geo-service/internal/routing"
	"geo-service/internal/storage"
)

const (
	defaultRouteAlternatives = 1
	maxRouteAlternatives     = 3
)

// RouteService computes routes, caches results, persists them, and fires events.
type RouteService struct {
	engine *routing.Engine
	redis  *cache.Redis
	bus    *events.Bus
	pg     *storage.Postgres
}

func NewRouteService(engine *routing.Engine, redis *cache.Redis, bus *events.Bus, pg *storage.Postgres) *RouteService {
	return &RouteService{engine: engine, redis: redis, bus: bus, pg: pg}
}

// Calculate returns suggested routes for the given request.
// If TripID is provided, the primary route polyline is cached under trip:{id}:route
// so the GPS service can use it for deviation detection.
func (s *RouteService) Calculate(ctx context.Context, req *model.RouteRequest) (*model.RouteResponse, error) {
	mode, err := routing.NormalizeMode(routeMode(req))
	if err != nil {
		return nil, err
	}
	alternatives := normalizeAlternatives(req.Alternatives)
	key := cache.RouteKey(string(mode), alternatives, req.StartLat, req.StartLng, req.EndLat, req.EndLng)

	var cached model.RouteResponse
	if err := s.redis.GetRoute(ctx, key, &cached); err == nil && cached.Distance > 0 {
		if req.TripID > 0 {
			_ = s.redis.SetTripRoute(ctx, req.TripID, &cached)
		}
		s.persistRoute(req, &cached)
		return &cached, nil
	}

	results := s.engine.CalculateAlternatives(req.StartLat, req.StartLng, req.EndLat, req.EndLng, mode, alternatives)
	resp := buildRouteResponse(mode, results)

	_ = s.redis.SetRoute(ctx, key, resp)

	if req.TripID > 0 {
		_ = s.redis.SetTripRoute(ctx, req.TripID, resp)
	}

	s.persistRoute(req, resp)
	s.publishRouteCalculated(req.TripID, resp)

	return resp, nil
}

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

func buildRouteResponse(mode routing.TransportMode, routes []*routing.Route) *model.RouteResponse {
	opts := make([]model.RouteOption, 0, len(routes))
	for i, r := range routes {
		path := make([][3]float64, len(r.Points))
		for j, p := range r.Points {
			path[j] = [3]float64{p.Lat, p.Lng, p.Alt}
		}
		opt := model.RouteOption{
			ID:          i + 1,
			Mode:        string(mode),
			IsPrimary:   i == 0,
			DistanceKm:  r.Distance,
			DurationMin: r.Duration,
			Polyline:    r.Polyline,
			Path:        path,
		}
		opts = append(opts, opt)
	}

	resp := &model.RouteResponse{
		Mode:   string(mode),
		Routes: opts,
	}
	if len(opts) > 0 {
		resp.Primary = opts[0]
		resp.Distance = opts[0].DistanceKm
		resp.Duration = opts[0].DurationMin
		resp.Polyline = opts[0].Polyline
	}
	return resp
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
