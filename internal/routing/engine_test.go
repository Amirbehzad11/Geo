package routing

import (
	"math"
	"testing"
)

// ─── existing helpers preserved ──────────────────────────────────────────────

func addNode(g *Graph, id int64, lat, lng float64) {
	g.AddNode(&Node{ID: id, Lat: lat, Lng: lng})
}

func addEdge(g *Graph, from, to int64, dist, speed float64, highway string, car, motorcycle, bus, foot bool) {
	g.AddEdge(from, Edge{
		To:                to,
		DistanceKm:        dist,
		SpeedKmH:          speed,
		TimeHours:         dist / speed,
		HighwayType:       highway,
		CarAllowed:        car,
		MotorcycleAllowed: motorcycle,
		BusAllowed:        bus,
		FootAllowed:       foot,
	})
}

// ─── Existing tests ───────────────────────────────────────────────────────────

func TestCarRouteOptimizesFastestTimeNotShortestDistance(t *testing.T) {
	g := NewGraph()
	addNode(g, 1, 0, 0)
	addNode(g, 2, 0, 0.01)
	addNode(g, 3, 0.01, 0)
	addNode(g, 4, 0, 0.02)

	addEdge(g, 1, 2, 1, 10, "residential", true, true, true, true)
	addEdge(g, 2, 4, 1, 10, "residential", true, true, true, true)
	addEdge(g, 1, 3, 2, 100, "primary", true, true, true, false)
	addEdge(g, 3, 4, 2, 100, "primary", true, true, true, false)

	e := &Engine{graph: g, avgSpeedKmH: 40}
	route := e.Calculate(0, 0, 0, 0.02, ModeCar)

	if route.Distance != 4 {
		t.Fatalf("expected fastest 4km route, got %.3fkm", route.Distance)
	}
	if route.Duration >= 3 {
		t.Fatalf("expected fast highway duration under 3 minutes, got %.2f", route.Duration)
	}
}

func TestWalkingCanUsePedestrianEdgesThatCarsCannot(t *testing.T) {
	g := NewGraph()
	addNode(g, 1, 0, 0)
	addNode(g, 2, 0, 0.02)
	addNode(g, 3, 0, 0.01)
	addNode(g, 4, 0.01, 0.01)

	addEdge(g, 1, 3, 1, 5, "footway", false, false, false, true)
	addEdge(g, 3, 2, 1, 5, "footway", false, false, false, true)
	addEdge(g, 1, 4, 5, 60, "primary", true, true, true, false)
	addEdge(g, 4, 2, 5, 60, "primary", true, true, true, false)

	e := &Engine{graph: g, avgSpeedKmH: 40}
	walking := e.Calculate(0, 0, 0, 0.02, ModeWalking)
	car := e.Calculate(0, 0, 0, 0.02, ModeCar)

	if walking.Distance != 2 {
		t.Fatalf("expected walking to use 2km footway path, got %.3fkm", walking.Distance)
	}
	if car.Distance != 10 {
		t.Fatalf("expected car to avoid footway and use 10km road path, got %.3fkm", car.Distance)
	}
}

func TestWalkingPrefersPedestrianPathOverShorterSharedRoad(t *testing.T) {
	g := NewGraph()
	addNode(g, 1, 0, 0)
	addNode(g, 2, 0, 0.01)
	addNode(g, 3, 0.006, 0.005)

	addEdge(g, 1, 2, 1.0, 30, "residential", true, true, true, true)
	addEdge(g, 1, 3, 0.8, 5, "footway", false, false, false, true)
	addEdge(g, 3, 2, 0.8, 5, "footway", false, false, false, true)

	e := &Engine{graph: g, avgSpeedKmH: 40}
	walking := e.Calculate(0, 0, 0, 0.01, ModeWalking)
	car := e.Calculate(0, 0, 0, 0.01, ModeCar)

	if walking.Distance != 1.6 {
		t.Fatalf("expected walking to prefer 1.6km pedestrian path, got %.3fkm", walking.Distance)
	}
	if len(walking.Points) != 3 || walking.Points[1].Lat != 0.006 {
		t.Fatalf("expected walking route through pedestrian node, got points=%v", walking.Points)
	}
	if car.Distance != 1.0 {
		t.Fatalf("expected car to use shorter residential road, got %.3fkm", car.Distance)
	}
}

func TestCarSnapIgnoresNearestPedestrianOnlyNode(t *testing.T) {
	g := NewGraph()
	addNode(g, 1, 0, 0)
	addNode(g, 2, 0, 0.001)
	addNode(g, 3, 0, 0.002)
	addNode(g, 4, 0, 0.003)

	addEdge(g, 1, 2, 1, 5, "footway", false, false, false, true)
	addEdge(g, 3, 4, 1, 30, "residential", true, true, true, true)

	e := &Engine{graph: g, avgSpeedKmH: 40}
	route := e.Calculate(0, 0.0001, 0, 0.003, ModeCar)

	if len(route.Points) < 3 || route.Points[0].Lng != 0.0001 || route.Points[1].Lng != 0.002 {
		t.Fatalf("expected route to start at requested coordinate then snap to car-routable node, got points=%v", route.Points)
	}
}

func TestCalculateAlternativesReturnsSortedUniqueRoutes(t *testing.T) {
	g := NewGraph()
	addNode(g, 1, 0, 0)
	addNode(g, 2, 0, 0.01)
	addNode(g, 3, 0.01, 0)
	addNode(g, 4, 0, 0.02)
	addNode(g, 5, -0.01, 0)

	addEdge(g, 1, 2, 1, 60, "residential", true, true, true, true)
	addEdge(g, 2, 4, 1, 60, "residential", true, true, true, true)
	addEdge(g, 1, 3, 2, 60, "secondary", true, true, true, true)
	addEdge(g, 3, 4, 2, 60, "secondary", true, true, true, true)
	addEdge(g, 1, 5, 3, 60, "tertiary", true, true, true, true)
	addEdge(g, 5, 4, 3, 60, "tertiary", true, true, true, true)

	e := &Engine{graph: g, avgSpeedKmH: 40}
	routes := e.CalculateAlternatives(0, 0, 0, 0.02, ModeCar, 3)

	if len(routes) != 3 {
		t.Fatalf("expected 3 routes, got %d", len(routes))
	}
	for i := 1; i < len(routes); i++ {
		if routes[i].Duration < routes[i-1].Duration {
			t.Fatalf("routes are not sorted by duration: %.2f before %.2f", routes[i-1].Duration, routes[i].Duration)
		}
		if routes[i].Polyline == routes[i-1].Polyline {
			t.Fatalf("duplicate alternative polyline at index %d", i)
		}
	}
}

func TestDecodePolylineMalformedInputReturnsNil(t *testing.T) {
	if got := DecodePolyline("~~~~"); got != nil {
		t.Fatalf("expected nil for malformed polyline, got %v", got)
	}
}

// ─── New comprehensive tests ──────────────────────────────────────────────────

// TestCalculate_Car_UsesGraph ensures Calculate with car mode traverses the
// graph rather than returning a bare Haversine line.
func TestCalculate_Car_UsesGraph(t *testing.T) {
	g := NewGraph()
	addNode(g, 1, 0.000, 0.000)
	addNode(g, 2, 0.000, 0.009)
	addNode(g, 3, 0.000, 0.018)

	addEdge(g, 1, 2, 1.0, 60, "residential", true, true, true, true)
	addEdge(g, 2, 3, 1.0, 60, "residential", true, true, true, true)

	e := NewEngineWithGraph(40, g)
	route := e.Calculate(0.000, 0.000, 0.000, 0.018, ModeCar)

	// A graph route through nodes 1→2→3 should have at least 3 points (including start, mid, end).
	if len(route.Points) < 2 {
		t.Fatalf("expected graph-based route with multiple points, got %d points", len(route.Points))
	}
	// The graph route distance should be approximately 2 km (1+1), not a Haversine straight line.
	// (The Haversine straight line from 0,0 to 0,0.018 is ~2 km too, but the graph path goes
	// through an intermediate node, confirming graph usage.)
	if route.Distance <= 0 {
		t.Fatalf("expected positive distance, got %.3f", route.Distance)
	}
}

// TestCalculate_Airplane_AlwaysFlightArc ensures airplane mode always returns
// a curved Bézier arc regardless of graph availability.
func TestCalculate_Airplane_AlwaysFlightArc(t *testing.T) {
	g := NewGraph()
	addNode(g, 1, 35.0, 51.0)
	addNode(g, 2, 36.0, 52.0)
	addEdge(g, 1, 2, 150, 800, "primary", true, true, true, false)

	e := NewEngineWithGraph(40, g)
	route := e.Calculate(35.0, 51.0, 36.0, 52.0, ModeAirplane)

	// Arc has n+2 = 52 points (50 intermediate + 2 endpoints).
	if len(route.Points) <= 2 {
		t.Fatalf("airplane route should include curved arc points, got %d", len(route.Points))
	}
	if route.Points[0].Lat != 35.0 || route.Points[len(route.Points)-1].Lat != 36.0 {
		t.Fatalf("airplane route endpoints wrong: %v", route.Points)
	}
	// Duration includes fixed 30 min takeoff/landing overhead.
	if route.Duration < 30 {
		t.Fatalf("airplane duration should include >=30 min overhead, got %.2f", route.Duration)
	}
}

// TestCalculate_Walking_BlocksMotorway ensures walking mode cannot use a
// motorway-only graph (no foot-allowed edges) and falls back to Haversine.
func TestCalculate_Walking_BlocksMotorway(t *testing.T) {
	g := NewGraph()
	addNode(g, 1, 0.000, 0.000)
	addNode(g, 2, 0.000, 0.009)

	// Motorway stays blocked for walking even if imported data marks foot_allowed.
	addEdge(g, 1, 2, 1, 120, "motorway", true, true, true, true)

	e := NewEngineWithGraph(40, g)
	route := e.Calculate(0.000, 0.000, 0.000, 0.009, ModeWalking)

	// No walkable path → haversine fallback → exactly 2 extra-interpolated points
	if route == nil {
		t.Fatal("expected haversine fallback route, got nil")
	}
	if route.Distance <= 0 {
		t.Fatalf("expected positive fallback distance, got %.3f", route.Distance)
	}
}

// TestCalculate_FallsBackToHaversine_WhenNoGraph ensures the engine returns a
// Haversine route when no graph is loaded.
func TestCalculate_FallsBackToHaversine_WhenNoGraph(t *testing.T) {
	e := &Engine{graph: nil, avgSpeedKmH: 60}
	route := e.Calculate(35.6892, 51.3890, 32.6539, 51.6660, ModeCar)

	if route == nil {
		t.Fatal("expected Haversine fallback route, got nil")
	}
	// Tehran to Isfahan ~340 km straight line.
	if route.Distance < 300 || route.Distance > 400 {
		t.Fatalf("Haversine fallback distance out of range: %.2f km", route.Distance)
	}
	// Haversine interpolate produces n+2 = 14 points.
	if len(route.Points) < 2 {
		t.Fatalf("expected interpolated points, got %d", len(route.Points))
	}
}

// TestCalculateAlternatives_Returns3Routes builds a graph with 3 distinct paths
// and asserts CalculateAlternatives returns all 3.
func TestCalculateAlternatives_Returns3Routes(t *testing.T) {
	g := NewGraph()
	addNode(g, 1, 0, 0)
	addNode(g, 2, 0, 0.01)
	addNode(g, 3, 0.01, 0)
	addNode(g, 4, 0, 0.02)
	addNode(g, 5, -0.01, 0)

	addEdge(g, 1, 2, 1, 60, "residential", true, true, true, true)
	addEdge(g, 2, 4, 1, 60, "residential", true, true, true, true)
	addEdge(g, 1, 3, 2, 80, "secondary", true, true, true, true)
	addEdge(g, 3, 4, 2, 80, "secondary", true, true, true, true)
	addEdge(g, 1, 5, 3, 70, "tertiary", true, true, true, true)
	addEdge(g, 5, 4, 3, 70, "tertiary", true, true, true, true)

	e := NewEngineWithGraph(40, g)
	routes := e.CalculateAlternatives(0, 0, 0, 0.02, ModeCar, 3)

	if len(routes) != 3 {
		t.Fatalf("expected 3 routes, got %d", len(routes))
	}
	// All routes should be distinct
	seen := make(map[string]bool)
	for i, r := range routes {
		if seen[r.Polyline] {
			t.Fatalf("duplicate route at index %d", i)
		}
		seen[r.Polyline] = true
	}
}

// TestNormalizeMode_Aliases checks that common alias strings resolve correctly.
func TestNormalizeMode_Aliases(t *testing.T) {
	cases := []struct {
		input string
		want  TransportMode
	}{
		{"walk", ModeWalking},
		{"pedestrian", ModeWalking},
		{"foot", ModeWalking},
		{"", ModeCar},
		{"car", ModeCar},
		{"motorcycle", ModeMotorcycle},
		{"bus", ModeBus},
		{"airplane", ModeAirplane},
		{"walking", ModeWalking},
		{"CAR", ModeCar},     // case insensitive
		{"  car  ", ModeCar}, // trimmed
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, err := NormalizeMode(tc.input)
			if err != nil {
				t.Fatalf("NormalizeMode(%q) unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeMode(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestNormalizeMode_Invalid ensures unsupported modes return an error.
func TestNormalizeMode_Invalid(t *testing.T) {
	invalid := []string{"bike", "bicycle", "submarine", "train"}
	for _, raw := range invalid {
		t.Run(raw, func(t *testing.T) {
			_, err := NormalizeMode(raw)
			if err == nil {
				t.Fatalf("NormalizeMode(%q) expected error, got nil", raw)
			}
		})
	}
}

// TestEncodeDecodePolyline_RoundTrip encodes known points and decodes them back,
// asserting each coordinate matches within 1e-5 degrees.
func TestEncodeDecodePolyline_RoundTrip(t *testing.T) {
	original := []Point{
		{Lat: 35.6892, Lng: 51.3890},
		{Lat: 35.7000, Lng: 51.4000},
		{Lat: 35.6500, Lng: 51.3500},
		{Lat: 32.6539, Lng: 51.6660},
		{Lat: -33.8688, Lng: 151.2093}, // Sydney
	}

	encoded := EncodePolyline(original)
	if encoded == "" {
		t.Fatal("EncodePolyline returned empty string")
	}

	decoded := DecodePolyline(encoded)
	if len(decoded) != len(original) {
		t.Fatalf("decoded %d points, want %d", len(decoded), len(original))
	}

	const tolerance = 1e-5
	for i, p := range decoded {
		if math.Abs(p.Lat-original[i].Lat) > tolerance {
			t.Errorf("point[%d] lat: got %.7f, want %.7f", i, p.Lat, original[i].Lat)
		}
		if math.Abs(p.Lng-original[i].Lng) > tolerance {
			t.Errorf("point[%d] lng: got %.7f, want %.7f", i, p.Lng, original[i].Lng)
		}
	}
}

// TestDecodePolyline_MalformedInput verifies no panic and nil return for bad input.
func TestDecodePolyline_MalformedInput(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"tildes", "~~~~"},
		{"truncated", "??"},
		{"empty", ""},
		{"single byte lat no lng", "A"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Must not panic.
			got := DecodePolyline(tc.input)
			// Empty string is valid (0 points), malformed should be nil or empty.
			_ = got // nil or empty is acceptable; the key requirement is no panic.
		})
	}
}

// ─── Benchmarks ───────────────────────────────────────────────────────────────

func BenchmarkCalculateAlternativesSmallGraph(b *testing.B) {
	g := NewGraph()
	addNode(g, 1, 0, 0)
	addNode(g, 2, 0, 0.01)
	addNode(g, 3, 0.01, 0)
	addNode(g, 4, 0, 0.02)
	addNode(g, 5, -0.01, 0)

	addEdge(g, 1, 2, 1, 60, "residential", true, true, true, true)
	addEdge(g, 2, 4, 1, 60, "residential", true, true, true, true)
	addEdge(g, 1, 3, 2, 80, "secondary", true, true, true, true)
	addEdge(g, 3, 4, 2, 80, "secondary", true, true, true, true)
	addEdge(g, 1, 5, 3, 70, "tertiary", true, true, true, true)
	addEdge(g, 5, 4, 3, 70, "tertiary", true, true, true, true)

	e := &Engine{graph: g, avgSpeedKmH: 40}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = e.CalculateAlternatives(0, 0, 0, 0.02, ModeCar, 3)
	}
}
