package routing

import (
	"context"
	"sync"

	"geo-service/internal/utils"
)

// MultiModalLeg is one segment of a multi-modal journey.
type MultiModalLeg struct {
	Mode  TransportMode
	Route *Route
}

// railSnapKm is the maximum distance from a point to the nearest rail node.
// Train stations can be far from arbitrary origins, so we use a generous limit.
const railSnapKm = 50.0

// transitSnapKm is the largest distance an origin/destination can be from the
// nearest bus stop or metro station. 5km covers a long walk to a major corridor;
// anything further almost certainly means the user is outside the served cities.
const transitSnapKm = 5.0

// transitBoardingPenaltyMin is added to the duration of each transit leg to
// represent average wait time + boarding. We do not bake it into edge weights
// (which would mis-score same-line continuations).
const transitBoardingPenaltyMin = 3.0

// ComputeTrainRoute returns a 3-leg walk→train→walk route.
// Returns nil when the rail graph is not loaded, no rail stations are reachable,
// or no rail path exists between the two nearest stations.
func (e *Engine) ComputeTrainRoute(ctx context.Context, startLat, startLng, endLat, endLng float64) []*MultiModalLeg {
	if e.railGraph == nil {
		return nil
	}

	trainProfile := profiles[ModeTrain]

	originStation := e.railGraph.NearestRoutableNodeWithin(startLat, startLng, railSnapKm, trainProfile.edgeAllowed)
	destStation := e.railGraph.NearestRoutableNodeWithin(endLat, endLng, railSnapKm, trainProfile.edgeAllowed)
	if originStation == nil || destStation == nil || originStation.ID == destStation.ID {
		return nil
	}

	trainRoute := e.computeRailLeg(ctx, originStation, destStation)

	// Walk leg 1: origin → origin station.
	walkRoute1 := e.computeWalkLeg(ctx, startLat, startLng, originStation.Lat, originStation.Lng)

	// Walk leg 2: destination station → destination.
	walkRoute2 := e.computeWalkLeg(ctx, destStation.Lat, destStation.Lng, endLat, endLng)

	return []*MultiModalLeg{
		{Mode: ModeWalking, Route: walkRoute1},
		{Mode: ModeTrain, Route: trainRoute},
		{Mode: ModeWalking, Route: walkRoute2},
	}
}

func (e *Engine) computeWalkLeg(ctx context.Context, startLat, startLng, endLat, endLng float64) *Route {
	if e.graph != nil {
		routes := e.graphRoutesCtx(ctx, startLat, startLng, endLat, endLng, ModeWalking, 1)
		if len(routes) > 0 {
			return routes[0]
		}
	}
	return e.haversineRoute(startLat, startLng, endLat, endLng, ModeWalking)
}

func (e *Engine) computeRailLeg(ctx context.Context, from, to *Node) *Route {
	p := profiles[ModeTrain]
	paths, err := e.railGraph.KFastestPaths(ctx, from.ID, to.ID, 1, e.yenSpurCap, p.edgeAllowed, p.edgeSpeed, p.heuristicSpeedKmH)
	if err == nil && len(paths) > 0 {
		return e.pathToRouteWithNameResolver(paths[0], from.Lat, from.Lng, to.Lat, to.Lng, ModeTrain, e.railGraph.NameFor)
	}
	// Fallback: straight-line estimate at train speed.
	// This handles disconnected rail network components (e.g. different metro
	// lines with no graph transfer edge between them).
	return e.haversineRoute(from.Lat, from.Lng, to.Lat, to.Lng, ModeTrain)
}

// ComputeTransitRoute returns a walk → (bus/metro chain) → walk multi-modal
// journey across a city's public-transport network. The middle section is
// split into one MultiModalLeg per line / transfer segment so clients can
// render "take Bus 5, transfer, take Metro 2" naturally.
//
// Returns nil if no transit graph is loaded or no path is found within
// transitSnapKm of both endpoints.
func (e *Engine) ComputeTransitRoute(ctx context.Context, startLat, startLng, endLat, endLng float64) []*MultiModalLeg {
	if e.transitGraph == nil {
		return nil
	}

	p := profiles[ModePublicTransport]
	originStop := e.transitGraph.NearestRoutableNodeWithin(startLat, startLng, transitSnapKm, p.edgeAllowed)
	destStop := e.transitGraph.NearestRoutableNodeWithin(endLat, endLng, transitSnapKm, p.edgeAllowed)
	if originStop == nil || destStop == nil || originStop.ID == destStop.ID {
		return nil
	}

	path, err := e.transitGraph.AStar(originStop.ID, destStop.ID, p.edgeAllowed, p.edgeSpeed)
	if err != nil || path == nil || len(path.Edges) == 0 {
		return nil
	}

	legs := make([]*MultiModalLeg, 0, 4)

	// Walk leg 1: origin → first stop.
	walk1 := e.computeWalkLeg(ctx, startLat, startLng, originStop.Lat, originStop.Lng)
	if walk1 != nil && walk1.Distance > 0 {
		legs = append(legs, &MultiModalLeg{Mode: ModeWalking, Route: walk1})
	}

	// Warm the polyline cache for every non-transfer edge on this path so the
	// transit legs render along real streets and rails. Done in parallel to
	// keep per-request latency bounded; each call holds a small allocation
	// budget rather than the full Iran-graph traversal cost.
	e.warmTransitPolylines(ctx, path)

	// Transit legs: one per contiguous run of edges sharing the same line.
	legs = append(legs, e.splitTransitLegs(path)...)

	// Walk leg 2: last stop → destination.
	walk2 := e.computeWalkLeg(ctx, destStop.Lat, destStop.Lng, endLat, endLng)
	if walk2 != nil && walk2.Distance > 0 {
		legs = append(legs, &MultiModalLeg{Mode: ModeWalking, Route: walk2})
	}

	return legs
}

// warmTransitPolylines fills the transit graph's per-edge polyline cache for
// every non-transfer edge along path. Transfer edges keep their straight line
// because they represent <300m walks where straight is already close to truth.
//
// Calls are parallelised with a small worker pool to keep latency bounded
// without exploding memory: each A* on the Iran road graph can allocate tens
// of MB, so we cap concurrency at 4. After the first call for an edge the
// result is cached, so subsequent requests touching the same line segment
// pay no A* cost.
func (e *Engine) warmTransitPolylines(ctx context.Context, path *PathResult) {
	if path == nil || e.transitGraph == nil {
		return
	}
	type job struct {
		from, to *Node
		kind     HighwayKind
	}
	var pending []job
	for i, ed := range path.Edges {
		if ed.Kind == HWTransfer {
			continue
		}
		from := path.Nodes[i]
		to := path.Nodes[i+1]
		if _, ok := e.transitGraph.EdgePolyline(from.ID, to.ID); ok {
			continue
		}
		pending = append(pending, job{from: from, to: to, kind: ed.Kind})
	}
	if len(pending) == 0 {
		return
	}

	const maxWorkers = 4
	workers := len(pending)
	if workers > maxWorkers {
		workers = maxWorkers
	}
	ch := make(chan job, len(pending))
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				if ctx.Err() != nil {
					return
				}
				e.ensureEdgePolyline(ctx, j.from.ID, j.to.ID, j.kind, j.from.Lat, j.from.Lng, j.to.Lat, j.to.Lng)
			}
		}()
	}
	for _, j := range pending {
		ch <- j
	}
	close(ch)
	wg.Wait()
}

// splitTransitLegs walks the A* path and groups consecutive edges that share
// the same line label (NameIdx) into a single leg. Transfer edges become their
// own walking legs so the rendered itinerary clearly shows mode changes.
func (e *Engine) splitTransitLegs(path *PathResult) []*MultiModalLeg {
	if path == nil || len(path.Edges) == 0 || len(path.Nodes) == 0 {
		return nil
	}

	legs := make([]*MultiModalLeg, 0, 3)
	nameFor := e.transitGraph.NameFor

	segStart := 0
	for i := 1; i <= len(path.Edges); i++ {
		breakHere := i == len(path.Edges) ||
			path.Edges[i].NameIdx != path.Edges[segStart].NameIdx ||
			edgeMode(path.Edges[i].Kind) != edgeMode(path.Edges[segStart].Kind)
		if !breakHere {
			continue
		}

		// segStart..i-1 is one segment.
		segEdges := path.Edges[segStart:i]
		// Nodes for this segment: the node BEFORE segStart through the node
		// AFTER (i-1). Since path.Nodes has len(Edges)+1 elements, segNodes is
		// path.Nodes[segStart : i+1].
		segNodes := path.Nodes[segStart : i+1]
		legs = append(legs, e.buildTransitSegmentLeg(segNodes, segEdges, nameFor))
		segStart = i
	}
	return legs
}

func edgeMode(kind HighwayKind) TransportMode {
	switch kind {
	case HWSubway, HWLightRail, HWTram:
		return ModeTrain // metro/light rail surface as "train" leg mode
	case HWBusRoute:
		return ModeBus
	case HWTransfer:
		return ModeWalking
	default:
		return ModePublicTransport
	}
}

func (e *Engine) buildTransitSegmentLeg(nodes []*Node, edges []Edge, nameFor func(uint32) string) *MultiModalLeg {
	if len(nodes) < 2 || len(edges) == 0 {
		return nil
	}

	totalKm := 0.0
	totalHours := 0.0
	// Expand each edge into its real polyline when one was pre-computed via
	// EnrichTransitGeometry; otherwise fall back to the straight node-to-node
	// segment so the leg always renders something usable.
	points := make([]Point, 0, len(nodes)*4)
	appendPoint(&points, Point{Lat: nodes[0].Lat, Lng: nodes[0].Lng})
	for i, ed := range edges {
		fromID := nodes[i].ID
		toID := nodes[i+1].ID
		if poly, ok := e.transitGraph.EdgePolyline(fromID, toID); ok && len(poly) >= 2 {
			// Skip the first point of the polyline because it already exists as
			// the last appended point (either the previous edge's tail or the
			// segment's starting node).
			for _, p := range poly[1:] {
				appendPoint(&points, p)
			}
		} else {
			appendPoint(&points, Point{Lat: nodes[i+1].Lat, Lng: nodes[i+1].Lng})
		}
		totalKm += ed.DistanceKm
		totalHours += float64(ed.TimeHours)
	}

	mode := edgeMode(edges[0].Kind)
	durationMin := totalHours * 60
	// Boarding penalty applies to motorised transit legs but not walking transfers.
	if mode != ModeWalking {
		durationMin += transitBoardingPenaltyMin
	}

	lineName := nameFor(edges[0].NameIdx)

	instructions := make([]Instruction, 0, len(edges))
	for i, ed := range edges {
		from := nodes[i]
		to := nodes[i+1]
		instText := transitInstructionText(ed.Kind, lineName, i == 0)
		instructions = append(instructions, Instruction{
			Index:       i,
			Type:        transitInstructionType(ed.Kind, i == 0),
			Text:        instText,
			DistanceKm:  utils.Round(ed.DistanceKm, 3),
			DurationMin: utils.Round(float64(ed.TimeHours)*60, 2),
			Location:    Point{Lat: from.Lat, Lng: from.Lng},
			StreetName:  lineName,
		})
		_ = to
	}

	return &MultiModalLeg{
		Mode: mode,
		Route: &Route{
			Distance:     utils.Round(totalKm, 3),
			Duration:     utils.Round(durationMin, 2),
			Points:       points,
			Polyline:     EncodePolyline(points),
			Instructions: instructions,
		},
	}
}

func transitInstructionType(kind HighwayKind, first bool) string {
	switch kind {
	case HWTransfer:
		return "walk_transfer"
	case HWBusRoute:
		if first {
			return "board_bus"
		}
		return "continue_bus"
	case HWSubway, HWLightRail, HWTram:
		if first {
			return "board_metro"
		}
		return "continue_metro"
	default:
		return "continue"
	}
}

func transitInstructionText(kind HighwayKind, lineName string, first bool) string {
	switch kind {
	case HWTransfer:
		return "Walk to next stop"
	case HWBusRoute:
		if first {
			return "Board " + lineName
		}
		return "Stay on " + lineName
	case HWSubway, HWLightRail, HWTram:
		if first {
			return "Board " + lineName
		}
		return "Stay on " + lineName
	default:
		return lineName
	}
}
