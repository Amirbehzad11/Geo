package route

import (
	"context"
	"fmt"
	"sync"

	"geo-service/internal/utils"
)

const (
	maxWaypoints           = 50
	minWaypoints           = 2
	maxConcurrentRouteLegs = 4
)

// MultiRouteService computes routes through a waypoint list.
// It keeps the first waypoint as the origin, greedily reorders the remaining
// stops by nearest-neighbor, then runs each leg concurrently and combines the
// polylines into one path.
type MultiRouteService struct {
	route *RouteService
}

// NewMultiRouteService creates a MultiRouteService backed by an existing
// RouteService (reuses its engine, cache, and semaphore).
func NewMultiRouteService(route *RouteService) *MultiRouteService {
	return &MultiRouteService{route: route}
}

// Compute routes the optimized waypoint order concurrently, then stitches the
// results into a single MultiRouteResponse.
func (s *MultiRouteService) Compute(ctx context.Context, req *MultiRouteRequest) (*MultiRouteResponse, error) {
	if len(req.Waypoints) < minWaypoints {
		return nil, fmt.Errorf("at least %d waypoints required", minWaypoints)
	}
	if len(req.Waypoints) > maxWaypoints {
		return nil, fmt.Errorf("too many waypoints: max %d", maxWaypoints)
	}
	for i, wp := range req.Waypoints {
		if !utils.ValidCoords(wp.Lat, wp.Lng) {
			return nil, fmt.Errorf("waypoint[%d] has invalid coordinates (lat=%v lng=%v)", i, wp.Lat, wp.Lng)
		}
	}

	orderedWaypoints := orderWaypointsNearestNeighbor(req.Waypoints)
	n := len(orderedWaypoints)
	legs := make([]legResult, n-1)

	// Compute all legs concurrently.
	var wg sync.WaitGroup
	legSlots := make(chan struct{}, maxConcurrentRouteLegs)
	for i := 0; i < n-1; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case legSlots <- struct{}{}:
				defer func() { <-legSlots }()
			case <-ctx.Done():
				legs[i] = legResult{err: ctx.Err(), from: orderedWaypoints[i], to: orderedWaypoints[i+1]}
				return
			}
			from := orderedWaypoints[i]
			to := orderedWaypoints[i+1]
			resp, err := s.route.Calculate(ctx, &RouteRequest{
				TripID:   req.TripID,
				StartLat: from.Lat,
				StartLng: from.Lng,
				EndLat:   to.Lat,
				EndLng:   to.Lng,
				Mode:     req.Mode,
			})
			legs[i] = legResult{resp: resp, err: err, from: from, to: to}
		}(i)
	}
	wg.Wait()

	// Check for errors (report the first one found in order).
	for i, l := range legs {
		if l.err != nil {
			// Translate service errors to user-friendly messages.
			return nil, fmt.Errorf("leg %d->%d (%s -> %s): %w",
				i, i+1, labelOf(orderedWaypoints[i]), labelOf(orderedWaypoints[i+1]), l.err)
		}
		if l.resp == nil {
			return nil, fmt.Errorf("leg %d→%d: no route found", i, i+1)
		}
	}

	// Build per-leg output and collect polylines for stitching.
	outLegs := make([]MultiRouteLeg, len(legs))
	polylines := make([]string, len(legs))
	var totalDist, totalDur float64

	for i, l := range legs {
		outLegs[i] = MultiRouteLeg{
			From:        legs[i].from,
			To:          legs[i].to,
			DistanceKm:  l.resp.Distance,
			DurationMin: l.resp.Duration,
		}
		polylines[i] = l.resp.Polyline
		totalDist += l.resp.Distance
		totalDur += l.resp.Duration
	}

	return &MultiRouteResponse{
		TotalDistanceKm:  totalDist,
		TotalDurationMin: totalDur,
		Polyline:         utils.CombinePolylines(polylines),
		Mode:             req.Mode,
		Legs:             outLegs,
	}, nil
}

// internal helpers

type legResult struct {
	from Waypoint
	to   Waypoint
	resp *RouteResponse
	err  error
}

func labelOf(wp Waypoint) string {
	if wp.Label != "" {
		return wp.Label
	}
	return fmt.Sprintf("(%.4f,%.4f)", wp.Lat, wp.Lng)
}

func orderWaypointsNearestNeighbor(waypoints []Waypoint) []Waypoint {
	if len(waypoints) <= 1 {
		return append([]Waypoint(nil), waypoints...)
	}

	ordered := make([]Waypoint, 0, len(waypoints))
	remaining := append([]Waypoint(nil), waypoints...)

	ordered = append(ordered, remaining[0])
	remaining = remaining[1:]
	current := ordered[0]

	for len(remaining) > 0 {
		bestIdx := 0
		bestDist := utils.Haversine(current.Lat, current.Lng, remaining[0].Lat, remaining[0].Lng)
		for i := 1; i < len(remaining); i++ {
			dist := utils.Haversine(current.Lat, current.Lng, remaining[i].Lat, remaining[i].Lng)
			if dist < bestDist {
				bestDist = dist
				bestIdx = i
			}
		}

		next := remaining[bestIdx]
		ordered = append(ordered, next)
		current = next
		remaining = append(remaining[:bestIdx], remaining[bestIdx+1:]...)
	}

	return ordered
}
