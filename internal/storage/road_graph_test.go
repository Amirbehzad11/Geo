package storage

import (
	"testing"

	"geo-service/internal/routing"
)

func TestAddRoadGraphEdgesAddsFootOnlyReverseForVehicleOneway(t *testing.T) {
	g := routing.NewGraph()
	edge := routing.Edge{
		To:         2,
		DistanceKm: 1,
		SpeedKmH:   30,
		Kind:       routing.ParseHighwayKind("residential"),
		Flags:      routing.FlagCar | routing.FlagMotorcycle | routing.FlagBus | routing.FlagFoot,
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
	if reverse.Flags.Has(routing.FlagCar) || reverse.Flags.Has(routing.FlagMotorcycle) || reverse.Flags.Has(routing.FlagBus) || !reverse.Flags.Has(routing.FlagFoot) {
		t.Fatalf("reverse edge should be foot-only, got flags=%08b", reverse.Flags)
	}
}

func TestAddRoadGraphEdgesRespectsOnewayPedestrianWays(t *testing.T) {
	g := routing.NewGraph()
	edge := routing.Edge{
		To:         2,
		DistanceKm: 1,
		SpeedKmH:   5,
		Kind:       routing.ParseHighwayKind("footway"),
		Flags:      routing.FlagFoot,
	}

	count := addRoadGraphEdges(g, 1, 2, edge, false)

	if count != 1 {
		t.Fatalf("expected only the tagged one-way footway edge, got %d", count)
	}
	if len(g.Edges[2]) != 0 {
		t.Fatalf("expected no reverse edge for one-way footway, got %+v", g.Edges[2])
	}
}
