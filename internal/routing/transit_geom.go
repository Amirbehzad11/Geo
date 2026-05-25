package routing

import (
	"context"
)

// transitSnapKmForGeom is how far we will look for the nearest routable node
// when snapping a bus stop or metro station onto the road / rail graph. Stops
// are usually <50m from a road, but station entrances can be deeper inside a
// pedestrian plaza, so 500m gives us comfortable margin without snapping to
// the wrong street.
const transitSnapKmForGeom = 0.5

// ensureEdgePolyline computes and caches the real-world geometry for a single
// transit edge if one isn't already cached. It is called lazily during route
// construction so we never pay the cost of A*-ing every bus segment at
// startup (which can OOM on memory-tight machines).
//
// Safe to call concurrently. Concurrent callers for the same edge may both
// compute the polyline once and race to store it — that's harmless waste.
func (e *Engine) ensureEdgePolyline(ctx context.Context, fromID, toID int64, kind HighwayKind, fromLat, fromLng, toLat, toLng float64) {
	if e.transitGraph == nil || kind == HWTransfer {
		return
	}
	if _, ok := e.transitGraph.EdgePolyline(fromID, toID); ok {
		return
	}
	poly := computeUnderlyingPolyline(ctx, transitGeomJob{
		fromID:  fromID,
		toID:    toID,
		fromLat: fromLat,
		fromLng: fromLng,
		toLat:   toLat,
		toLng:   toLng,
		kind:    kind,
	}, e.graph, e.railGraph)
	if len(poly) < 2 {
		return
	}
	e.transitGraph.SetEdgePolyline(fromID, toID, poly)
}

type transitGeomJob struct {
	fromID, toID     int64
	fromLat, fromLng float64
	toLat, toLng     float64
	kind             HighwayKind
}

func computeUnderlyingPolyline(ctx context.Context, j transitGeomJob, road, rail *Graph) []Point {
	g, prof := graphAndProfileFor(j.kind, road, rail)
	if g == nil {
		return nil
	}

	startNode := g.NearestRoutableNodeWithin(j.fromLat, j.fromLng, transitSnapKmForGeom, prof.edgeAllowed)
	endNode := g.NearestRoutableNodeWithin(j.toLat, j.toLng, transitSnapKmForGeom, prof.edgeAllowed)
	if startNode == nil || endNode == nil || startNode.ID == endNode.ID {
		return nil
	}

	path, err := g.aStar(ctx, startNode.ID, endNode.ID, prof.edgeAllowed, prof.edgeSpeed, prof.heuristicSpeedKmH, nil, nil)
	if err != nil || path == nil || len(path.Nodes) == 0 {
		return nil
	}

	// Build polyline: stop coordinates → snapped-path nodes → next stop coordinates.
	// The snap legs at the start/end are short (<500m) and avoid teleport jumps.
	poly := make([]Point, 0, len(path.Nodes)+2)
	poly = append(poly, Point{Lat: j.fromLat, Lng: j.fromLng})
	for _, n := range path.Nodes {
		appendPoint(&poly, Point{Lat: n.Lat, Lng: n.Lng})
	}
	appendPoint(&poly, Point{Lat: j.toLat, Lng: j.toLng})
	if len(poly) < 2 {
		return nil
	}
	return poly
}

// graphAndProfileFor picks which underlying graph + mode profile to use when
// reconstructing the real-world polyline for a transit edge.
//   - bus segments traverse the road graph using the bus profile (driveable
//     roads, bus speeds)
//   - metro / light rail / tram segments traverse the rail graph using the
//     train profile (rail-only edges)
func graphAndProfileFor(kind HighwayKind, road, rail *Graph) (*Graph, modeProfile) {
	switch kind {
	case HWBusRoute:
		if road == nil {
			return nil, modeProfile{}
		}
		return road, profiles[ModeBus]
	case HWSubway, HWLightRail, HWTram:
		if rail == nil {
			return nil, modeProfile{}
		}
		return rail, profiles[ModeTrain]
	default:
		return nil, modeProfile{}
	}
}
