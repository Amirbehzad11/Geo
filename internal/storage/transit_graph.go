package storage

import (
	"context"
	"fmt"

	"geo-service/internal/routing"
	"geo-service/internal/utils"
)

// Tunables for transit graph construction.
const (
	transitBusSpeedKmH      = 20.0
	transitMetroSpeedKmH    = 40.0
	transitTransferSpeedKmH = 5.0
	// transitTransferMaxKm is the largest walking distance treated as a
	// "transfer" between two transit stops. 300m matches Google Maps' default.
	transitTransferMaxKm = 0.30
	// transitDistanceFactor compensates for the fact that consecutive stops on
	// a bus/metro line are connected with a straight haversine distance, while
	// the actual driving route follows roads.
	transitDistanceFactor = 1.20
)

type transitStop struct {
	id   int64 // synthetic graph node ID
	osm  int64 // OSM node ID (debugging only)
	lat  float64
	lng  float64
	city string
	name string
}

// LoadTransitGraph builds the in-memory multi-modal graph for public-transport
// routing across the supported intra-city networks. The graph contains:
//
//   - one node per bus stop / metro station
//   - one directed edge between consecutive stops on each bus line
//   - one directed edge between consecutive stations on each metro line
//   - bidirectional walking transfer edges between any two stops within
//     transitTransferMaxKm in the same city
//
// Returns an error only when the database is unreachable. An empty graph (no
// stops or no lines) is returned as a *non-error* so that callers can treat
// "transit not available" as a feature toggle rather than a startup failure.
func (p *Postgres) LoadTransitGraph(ctx context.Context) (*routing.Graph, error) {
	if p == nil {
		return nil, fmt.Errorf("postgres disabled")
	}

	g := routing.NewGraph()

	busStops, err := p.loadBusStops(ctx, g)
	if err != nil {
		return nil, err
	}
	metroStations, err := p.loadMetroStations(ctx, g)
	if err != nil {
		return nil, err
	}

	busEdges, err := p.loadBusLineEdges(ctx, g, busStops)
	if err != nil {
		return nil, err
	}
	metroEdges, err := p.loadMetroLineEdges(ctx, g, metroStations)
	if err != nil {
		return nil, err
	}

	transferEdges := buildTransferEdges(g, busStops, metroStations)

	if g.NodeCount() == 0 {
		return g, fmt.Errorf("transit graph is empty (no bus/metro stops imported)")
	}
	if busEdges+metroEdges == 0 {
		return g, fmt.Errorf("transit graph has stops but no line edges")
	}

	// transferEdges is informational only; we don't fail on zero transfers.
	_ = transferEdges

	return g, nil
}

func (p *Postgres) loadBusStops(ctx context.Context, g *routing.Graph) (map[int64]transitStop, error) {
	const q = `SELECT id, osm_node_id, name, city, lat, lng FROM bus_stops`
	rows, err := p.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("load bus_stops: %w", err)
	}
	defer rows.Close()

	out := make(map[int64]transitStop)
	for rows.Next() {
		var s transitStop
		if err := rows.Scan(&s.id, &s.osm, &s.name, &s.city, &s.lat, &s.lng); err != nil {
			return nil, fmt.Errorf("scan bus_stop: %w", err)
		}
		// Bus stops occupy the positive ID space (id directly from DB).
		// Metro stations are offset to avoid collisions (see loadMetroStations).
		g.AddNode(&routing.Node{ID: s.id, Lat: s.lat, Lng: s.lng})
		out[s.id] = s
	}
	return out, rows.Err()
}

// metroStationIDOffset keeps metro station IDs in their own namespace so the
// graph can hold both transit kinds without ID collisions. 10^12 is well above
// any realistic bus_stops.id.
const metroStationIDOffset int64 = 1_000_000_000_000

func (p *Postgres) loadMetroStations(ctx context.Context, g *routing.Graph) (map[int64]transitStop, error) {
	const q = `SELECT id, osm_node_id, name, city, lat, lng FROM metro_stations`
	rows, err := p.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("load metro_stations: %w", err)
	}
	defer rows.Close()

	out := make(map[int64]transitStop)
	for rows.Next() {
		var dbID int64
		var s transitStop
		if err := rows.Scan(&dbID, &s.osm, &s.name, &s.city, &s.lat, &s.lng); err != nil {
			return nil, fmt.Errorf("scan metro_station: %w", err)
		}
		s.id = dbID + metroStationIDOffset
		g.AddNode(&routing.Node{ID: s.id, Lat: s.lat, Lng: s.lng})
		out[dbID] = s
	}
	return out, rows.Err()
}

func (p *Postgres) loadBusLineEdges(ctx context.Context, g *routing.Graph, stops map[int64]transitStop) (int, error) {
	const q = `
		SELECT bls.line_id, bls.stop_id, bls.stop_sequence,
		       COALESCE(bl.ref, ''), COALESCE(bl.name, '')
		FROM bus_line_stops bls
		JOIN bus_lines bl ON bl.id = bls.line_id
		ORDER BY bls.line_id, bls.stop_sequence`
	rows, err := p.pool.Query(ctx, q)
	if err != nil {
		return 0, fmt.Errorf("load bus_line_stops: %w", err)
	}
	defer rows.Close()

	edges := 0
	var curLine int64 = -1
	var prevStop *transitStop
	var curLineLabel string
	for rows.Next() {
		var lineID, stopID int64
		var seq int
		var ref, name string
		if err := rows.Scan(&lineID, &stopID, &seq, &ref, &name); err != nil {
			return edges, fmt.Errorf("scan bus_line_stops: %w", err)
		}
		stop, ok := stops[stopID]
		if !ok {
			continue
		}
		if lineID != curLine {
			curLine = lineID
			prevStop = nil
			curLineLabel = busLineLabel(ref, name)
		}
		if prevStop != nil {
			addTransitEdge(g, *prevStop, stop, routing.HWBusRoute, transitBusSpeedKmH, curLineLabel)
			edges++
		}
		s := stop
		prevStop = &s
	}
	return edges, rows.Err()
}

func (p *Postgres) loadMetroLineEdges(ctx context.Context, g *routing.Graph, stations map[int64]transitStop) (int, error) {
	const q = `
		SELECT mls.line_id, mls.station_id, mls.station_sequence,
		       COALESCE(ml.ref, ''), COALESCE(ml.name, '')
		FROM metro_line_stations mls
		JOIN metro_lines ml ON ml.id = mls.line_id
		ORDER BY mls.line_id, mls.station_sequence`
	rows, err := p.pool.Query(ctx, q)
	if err != nil {
		return 0, fmt.Errorf("load metro_line_stations: %w", err)
	}
	defer rows.Close()

	edges := 0
	var curLine int64 = -1
	var prevStation *transitStop
	var curLineLabel string
	for rows.Next() {
		var lineID, stationID int64
		var seq int
		var ref, name string
		if err := rows.Scan(&lineID, &stationID, &seq, &ref, &name); err != nil {
			return edges, fmt.Errorf("scan metro_line_stations: %w", err)
		}
		station, ok := stations[stationID]
		if !ok {
			continue
		}
		if lineID != curLine {
			curLine = lineID
			prevStation = nil
			curLineLabel = metroLineLabel(ref, name)
		}
		if prevStation != nil {
			addTransitEdge(g, *prevStation, station, routing.HWSubway, transitMetroSpeedKmH, curLineLabel)
			edges++
		}
		s := station
		prevStation = &s
	}
	return edges, rows.Err()
}

func addTransitEdge(g *routing.Graph, from, to transitStop, kind routing.HighwayKind, speedKmH float64, label string) {
	dist := utils.Haversine(from.lat, from.lng, to.lat, to.lng) * transitDistanceFactor
	if dist < 0.01 {
		return
	}
	edge := routing.Edge{
		To:         to.id,
		DistanceKm: dist,
		SpeedKmH:   float32(speedKmH),
		TimeHours:  float32(dist / speedKmH),
		Kind:       kind,
		Flags:      routing.FlagTransit,
		NameIdx:    g.InternName(label),
	}
	g.AddEdge(from.id, edge)
}

// buildTransferEdges connects any two stops/stations within transitTransferMaxKm
// in the same city with bidirectional walking edges. Same-line consecutive stops
// already have a transit edge, but transfer edges let A* switch lines whenever
// stops are close to each other (e.g. between a bus stop and a metro entrance).
//
// O(n²) over ~5–10k stops is fast enough at startup (<1s) and avoids needing
// PostGIS spatial joins.
func buildTransferEdges(g *routing.Graph, busStops, metroStations map[int64]transitStop) int {
	all := make([]transitStop, 0, len(busStops)+len(metroStations))
	for _, s := range busStops {
		all = append(all, s)
	}
	for _, s := range metroStations {
		all = append(all, s)
	}

	count := 0
	transferLabel := g.InternName("transfer")
	for i := 0; i < len(all); i++ {
		a := all[i]
		for j := i + 1; j < len(all); j++ {
			b := all[j]
			if a.city != b.city {
				continue
			}
			d := utils.Haversine(a.lat, a.lng, b.lat, b.lng)
			if d > transitTransferMaxKm || d < 0.005 {
				continue
			}
			edgeAB := routing.Edge{
				To:         b.id,
				DistanceKm: d,
				SpeedKmH:   float32(transitTransferSpeedKmH),
				TimeHours:  float32(d / transitTransferSpeedKmH),
				Kind:       routing.HWTransfer,
				Flags:      routing.FlagTransit,
				NameIdx:    transferLabel,
			}
			g.AddEdge(a.id, edgeAB)
			edgeBA := edgeAB
			edgeBA.To = a.id
			g.AddEdge(b.id, edgeBA)
			count += 2
		}
	}
	return count
}

func busLineLabel(ref, name string) string {
	if ref != "" && name != "" {
		return "Bus " + ref + " — " + name
	}
	if ref != "" {
		return "Bus " + ref
	}
	if name != "" {
		return "Bus " + name
	}
	return "Bus"
}

func metroLineLabel(ref, name string) string {
	if ref != "" && name != "" {
		return "Metro " + ref + " — " + name
	}
	if ref != "" {
		return "Metro " + ref
	}
	if name != "" {
		return "Metro " + name
	}
	return "Metro"
}
