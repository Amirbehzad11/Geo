package route

import (
	"context"
	"testing"

	"geo-service/internal/routing"
	"geo-service/internal/utils"
)

func TestOrderWaypointsNearestNeighbor(t *testing.T) {
	waypoints := []Waypoint{
		{Lat: 32.66, Lng: 51.67, Label: "اصفهان"},
		{Lat: 36.677024, Lng: 52.548294, Label: "مازندران"},
		{Lat: 35.70, Lng: 51.40, Label: "تهران"},
	}

	ordered := orderWaypointsNearestNeighbor(waypoints)

	want := []string{"اصفهان", "تهران", "مازندران"}
	if len(ordered) != len(want) {
		t.Fatalf("ordered waypoint count = %d, want %d", len(ordered), len(want))
	}
	for i, wp := range ordered {
		if wp.Label != want[i] {
			t.Fatalf("ordered[%d] = %q, want %q", i, wp.Label, want[i])
		}
	}
}

func TestMultiRouteService_ReordersWaypointsByNearestNeighbor(t *testing.T) {
	backend := &stubBackend{
		name: "test",
		fn: func(context.Context) (*RouteResponse, error) {
			return &RouteResponse{
				Distance: 10,
				Duration: 5,
				Mode:     string(routing.ModeCar),
				Polyline: utils.EncodePolyline([]utils.PolyPoint{
					{Lat: 32.66, Lng: 51.67},
					{Lat: 35.70, Lng: 51.40},
				}),
			}, nil
		},
	}

	routeSvc := NewRouteService(backend, nil, nil, nil, RouteServiceConfig{})
	svc := NewMultiRouteService(routeSvc)

	resp, err := svc.Compute(context.Background(), &MultiRouteRequest{
		Waypoints: []Waypoint{
			{Lat: 32.66, Lng: 51.67, Label: "اصفهان"},
			{Lat: 36.677024, Lng: 52.548294, Label: "مازندران"},
			{Lat: 35.70, Lng: 51.40, Label: "تهران"},
		},
		Mode: string(routing.ModeCar),
	})
	if err != nil {
		t.Fatalf("Compute() error = %v", err)
	}

	if len(resp.Legs) != 2 {
		t.Fatalf("expected 2 legs, got %d", len(resp.Legs))
	}
	if got := resp.Legs[0].From.Label; got != "اصفهان" {
		t.Fatalf("first leg starts at %q, want %q", got, "اصفهان")
	}
	if got := resp.Legs[0].To.Label; got != "تهران" {
		t.Fatalf("first leg ends at %q, want %q", got, "تهران")
	}
	if got := resp.Legs[1].From.Label; got != "تهران" {
		t.Fatalf("second leg starts at %q, want %q", got, "تهران")
	}
	if got := resp.Legs[1].To.Label; got != "مازندران" {
		t.Fatalf("second leg ends at %q, want %q", got, "مازندران")
	}
	if resp.TotalDistanceKm != 20 {
		t.Fatalf("total distance = %.1f, want 20", resp.TotalDistanceKm)
	}
}
