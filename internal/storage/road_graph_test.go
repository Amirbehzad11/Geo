package storage

import (
	"testing"

	"geo-service/internal/routing"
)

func TestAddRoadGraphEdgesAddsFootOnlyReverseForVehicleOneway(t *testing.T) {
	g := routing.NewGraph()
	edge := routing.Edge{
		To:                2,
		DistanceKm:        1,
		SpeedKmH:          30,
		HighwayType:       "residential",
		CarAllowed:        true,
		MotorcycleAllowed: true,
		BusAllowed:        true,
		FootAllowed:       true,
	}

	count := addRoadGraphEdges(g, 1, 2, edge, false)

	if count != 2 {
		t.Fatalf("expected forward edge plus foot reverse, got %d", count)
	}
	if len(g.Edges[1]) != 1 || len(g.Edges[2]) != 1 {
		t.Fatalf("expected one edge in each direction, got forward=%d reverse=%d", len(g.Edges[1]), len(g.Edges[2]))
	}
	reverse := g.Edges[2][0]
	if reverse.To != 1 {
		t.Fatalf("reverse edge should point back to node 1, got %d", reverse.To)
	}
	if reverse.CarAllowed || reverse.MotorcycleAllowed || reverse.BusAllowed || !reverse.FootAllowed {
		t.Fatalf("reverse edge should be foot-only, got %+v", reverse)
	}
}

func TestAddRoadGraphEdgesRespectsOnewayPedestrianWays(t *testing.T) {
	g := routing.NewGraph()
	edge := routing.Edge{
		To:          2,
		DistanceKm:  1,
		SpeedKmH:    5,
		HighwayType: "footway",
		FootAllowed: true,
	}

	count := addRoadGraphEdges(g, 1, 2, edge, false)

	if count != 1 {
		t.Fatalf("expected only the tagged one-way footway edge, got %d", count)
	}
	if len(g.Edges[2]) != 0 {
		t.Fatalf("expected no reverse edge for one-way footway, got %+v", g.Edges[2])
	}
}
