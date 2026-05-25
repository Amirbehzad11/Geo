package storage

import (
	"context"
	"fmt"
	"strings"

	"geo-service/internal/routing"
)

// LoadRailGraph builds the in-memory rail routing graph from rail_segments.
func (p *Postgres) LoadRailGraph(ctx context.Context) (*routing.Graph, error) {
	if p == nil {
		return nil, fmt.Errorf("postgres disabled")
	}

	q := fmt.Sprintf(`
		SELECT
			from_node_id, to_node_id,
			from_lat, from_lng, to_lat, to_lng,
			distance_km, speed_kmh, railway_type, COALESCE(name, ''),
			bidirectional
		FROM %s
		WHERE distance_km > 0`, quoteIdent("rail_segments"))

	rows, err := p.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("load rail_segments: %w", err)
	}
	defer rows.Close()

	g := routing.NewGraph()
	seenNodes := make(map[int64]bool)
	edgeCount := 0

	for rows.Next() {
		var (
			fromID, toID                   int64
			fromLat, fromLng, toLat, toLng float64
			distanceKm, speedKmH           float64
			railwayType, name              string
			bidirectional                  bool
		)
		if err := rows.Scan(
			&fromID, &toID,
			&fromLat, &fromLng, &toLat, &toLng,
			&distanceKm, &speedKmH, &railwayType, &name,
			&bidirectional,
		); err != nil {
			return nil, fmt.Errorf("scan rail segment: %w", err)
		}

		if speedKmH <= 0 {
			speedKmH = 80
		}
		if !seenNodes[fromID] {
			g.AddNode(&routing.Node{ID: fromID, Lat: fromLat, Lng: fromLng})
			seenNodes[fromID] = true
		}
		if !seenNodes[toID] {
			g.AddNode(&routing.Node{ID: toID, Lat: toLat, Lng: toLng})
			seenNodes[toID] = true
		}

		edge := routing.Edge{
			To:         toID,
			DistanceKm: distanceKm,
			SpeedKmH:   float32(speedKmH),
			TimeHours:  float32(distanceKm / speedKmH),
			Kind:       routing.ParseHighwayKind(railwayType),
			Flags:      routing.FlagTrain,
			NameIdx:    g.InternName(name),
		}
		edgeCount += addRoadGraphEdges(g, fromID, toID, edge, bidirectional)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rail_segments: %w", err)
	}
	if edgeCount == 0 {
		return nil, fmt.Errorf("rail_segments table is empty")
	}
	return g, nil
}

// LoadRoadGraph builds the in-memory routing graph from road_segments.
func (p *Postgres) LoadRoadGraph(ctx context.Context) (*routing.Graph, error) {
	return p.LoadRoadGraphRegions(ctx, nil)
}

// LoadRoadGraphRegions builds the graph from one or more province road tables.
func (p *Postgres) LoadRoadGraphRegions(ctx context.Context, regions []string) (*routing.Graph, error) {
	if p == nil {
		return nil, fmt.Errorf("postgres disabled")
	}

	tables, err := roadGraphTables(regions)
	if err != nil {
		return nil, err
	}

	g := routing.NewGraph()
	seenNodes := make(map[int64]bool)
	edgeCount := 0

	for _, table := range tables {
		count, err := p.loadRoadGraphTable(ctx, g, seenNodes, table)
		if err != nil {
			return nil, err
		}
		edgeCount += count
	}
	if edgeCount == 0 {
		return nil, fmt.Errorf("selected road segment tables are empty")
	}
	return g, nil
}

func roadGraphTables(regions []string) ([]string, error) {
	if len(regions) == 0 {
		return []string{"road_segments"}, nil
	}
	tables := make([]string, 0, len(regions))
	for _, region := range regions {
		region = strings.TrimSpace(region)
		if region == "" {
			continue
		}
		table, err := RoadSegmentsTableName(region)
		if err != nil {
			return nil, err
		}
		tables = append(tables, table)
	}
	if len(tables) == 0 {
		return []string{"road_segments"}, nil
	}
	return tables, nil
}

func (p *Postgres) loadRoadGraphTable(ctx context.Context, g *routing.Graph, seenNodes map[int64]bool, table string) (int, error) {
	q := fmt.Sprintf(`
		SELECT
			from_node_id, to_node_id,
			from_lat, from_lng, to_lat, to_lng,
			distance_km, speed_kmh, highway_type, COALESCE(name, ''),
			bidirectional, car_allowed, motorcycle_allowed, bus_allowed, foot_allowed
		FROM %s
		WHERE distance_km > 0`, quoteIdent(table))

	rows, err := p.pool.Query(ctx, q)
	if err != nil {
		return 0, fmt.Errorf("load %s: %w", table, err)
	}
	defer rows.Close()

	edgeCount := 0
	for rows.Next() {
		var (
			fromID, toID                   int64
			fromLat, fromLng, toLat, toLng float64
			distanceKm, speedKmH           float64
			highwayType, name              string
			bidirectional                  bool
			car, motorcycle, bus, foot     bool
		)
		if err := rows.Scan(
			&fromID, &toID,
			&fromLat, &fromLng, &toLat, &toLng,
			&distanceKm, &speedKmH, &highwayType, &name,
			&bidirectional, &car, &motorcycle, &bus, &foot,
		); err != nil {
			return 0, fmt.Errorf("scan road segment: %w", err)
		}

		if speedKmH <= 0 {
			speedKmH = 40
		}
		if !seenNodes[fromID] {
			g.AddNode(&routing.Node{ID: fromID, Lat: fromLat, Lng: fromLng})
			seenNodes[fromID] = true
		}
		if !seenNodes[toID] {
			g.AddNode(&routing.Node{ID: toID, Lat: toLat, Lng: toLng})
			seenNodes[toID] = true
		}

		// Build access flags from the four boolean columns.
		var flags routing.AccessFlags
		if car {
			flags |= routing.FlagCar
		}
		if motorcycle {
			flags |= routing.FlagMotorcycle
		}
		if bus {
			flags |= routing.FlagBus
		}
		if foot {
			flags |= routing.FlagFoot
		}

		edge := routing.Edge{
			To:         toID,
			DistanceKm: distanceKm,
			SpeedKmH:   float32(speedKmH),
			TimeHours:  float32(distanceKm / speedKmH),
			Kind:       routing.ParseHighwayKind(highwayType),
			Flags:      flags,
			NameIdx:    g.InternName(name), // intern into shared pool
		}
		edgeCount += addRoadGraphEdges(g, fromID, toID, edge, bidirectional)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate %s: %w", table, err)
	}
	return edgeCount, nil
}

func addRoadGraphEdges(g *routing.Graph, fromID, toID int64, edge routing.Edge, bidirectional bool) int {
	g.AddEdge(fromID, edge)
	count := 1

	if bidirectional {
		reverse := edge
		reverse.To = fromID
		g.AddEdge(toID, reverse)
		return count + 1
	}

	// Pedestrians may legally walk against vehicle one-way restrictions on most
	// road classes; purely-pedestrian infrastructure (footway, steps, etc.) has
	// its own direction data and does not need a synthetic reverse edge.
	if edge.Flags.Has(routing.FlagFoot) && edge.Kind.AllowsPedestriansAgainstFlow() {
		reverse := edge
		reverse.To = fromID
		// Strip vehicle access — only foot is permitted on the reverse direction.
		reverse.Flags = routing.FlagFoot
		g.AddEdge(toID, reverse)
		count++
	}

	return count
}
