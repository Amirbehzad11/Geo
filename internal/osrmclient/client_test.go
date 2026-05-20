package osrmclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"geo-service/internal/routing"
)

// fixtureResponse is a realistic OSRM route API response captured from a real server.
const fixtureResponse = `{
  "code": "Ok",
  "routes": [{
    "distance": 12500.0,
    "duration":  900.0,
    "geometry":  "abc123encoded",
    "legs": [{
      "steps": [
        {
          "distance": 500.0,
          "duration":  30.0,
          "name": "Main Street",
          "maneuver": {
            "type":     "depart",
            "modifier": "right",
            "location": [51.3890, 35.6892]
          }
        },
        {
          "distance": 11000.0,
          "duration":  800.0,
          "name": "Tehran Road",
          "maneuver": {
            "type":     "turn",
            "modifier": "left",
            "location": [51.4000, 35.7000]
          }
        },
        {
          "distance": 1000.0,
          "duration":   70.0,
          "name": "",
          "maneuver": {
            "type":     "arrive",
            "modifier": "straight",
            "location": [51.4100, 35.7100]
          }
        }
      ]
    }]
  }]
}`

// serve spins up a test HTTP server that always returns the given body with 200 OK.
func serve(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

// TestRoute_ParsesDistanceAndDuration checks unit conversions (m→km, s→min).
func TestRoute_ParsesDistanceAndDuration(t *testing.T) {
	srv := serve(t, fixtureResponse)
	defer srv.Close()

	resp, err := New(srv.URL, 0).Route(context.Background(), routing.ModeCar, 35.6892, 51.389, 35.71, 51.41)
	if err != nil {
		t.Fatalf("Route() error: %v", err)
	}

	// 12 500 m → 12.500 km
	if resp.Distance != 12.5 {
		t.Errorf("distance = %.3f, want 12.500", resp.Distance)
	}
	// 900 s → 15.00 min
	if resp.Duration != 15 {
		t.Errorf("duration = %.2f, want 15.00", resp.Duration)
	}
	if resp.Polyline != "abc123encoded" {
		t.Errorf("polyline = %q, want abc123encoded", resp.Polyline)
	}
	if resp.Mode != "car" {
		t.Errorf("mode = %q, want car", resp.Mode)
	}
}

// TestRoute_PrimaryIsSet checks that the primary field is populated correctly.
func TestRoute_PrimaryIsSet(t *testing.T) {
	srv := serve(t, fixtureResponse)
	defer srv.Close()

	resp, err := New(srv.URL, 0).Route(context.Background(), routing.ModeCar, 35.6892, 51.389, 35.71, 51.41)
	if err != nil {
		t.Fatalf("Route() error: %v", err)
	}
	if !resp.Primary.IsPrimary {
		t.Error("primary.is_primary should be true")
	}
	if resp.Primary.DistanceKm != resp.Distance {
		t.Errorf("primary distance mismatch: primary=%.3f top-level=%.3f", resp.Primary.DistanceKm, resp.Distance)
	}
	// Internal Routes field is populated so persistRoute can use it.
	if len(resp.Routes) == 0 {
		t.Error("internal Routes slice must be populated for persistence")
	}
}

// TestRoute_InstructionsArePersian verifies the full instruction-text pipeline.
func TestRoute_InstructionsArePersian(t *testing.T) {
	srv := serve(t, fixtureResponse)
	defer srv.Close()

	resp, err := New(srv.URL, 0).Route(context.Background(), routing.ModeCar, 35.6892, 51.389, 35.71, 51.41)
	if err != nil {
		t.Fatalf("Route() error: %v", err)
	}

	ins := resp.Primary.Instructions
	if len(ins) < 3 {
		t.Fatalf("expected ≥3 instructions, got %d", len(ins))
	}

	// Depart step: must start with the Persian departure phrase and include
	// "پس از X متر" to indicate distance until the next maneuver.
	dep := ins[0]
	if dep.Type != "depart" {
		t.Fatalf("ins[0].type = %q, want depart", dep.Type)
	}
	if !strings.Contains(dep.Text, "حرکت را شروع کنید") {
		t.Errorf("depart text missing start phrase: %q", dep.Text)
	}
	if !strings.Contains(dep.Text, "پس از") {
		t.Errorf("depart text missing distance hint: %q", dep.Text)
	}

	// Turn step: left turn must produce the Persian left-turn phrase.
	turn := ins[1]
	if turn.Type != "turn" {
		t.Fatalf("ins[1].type = %q, want turn", turn.Type)
	}
	if !strings.Contains(turn.Text, "به چپ بپیچید") {
		t.Errorf("left-turn text missing Persian phrase: %q", turn.Text)
	}

	// Arrive step.
	arr := ins[len(ins)-1]
	if arr.Type != "arrive" {
		t.Fatalf("last instruction type = %q, want arrive", arr.Type)
	}
	if arr.Text != "به مقصد رسیدید" {
		t.Errorf("arrive text = %q, want %q", arr.Text, "به مقصد رسیدید")
	}
}

// TestRoute_InstructionDistanceIncludesNextManeuver checks the lead-in text.
func TestRoute_InstructionDistanceIncludesNextManeuver(t *testing.T) {
	srv := serve(t, fixtureResponse)
	defer srv.Close()

	resp, err := New(srv.URL, 0).Route(context.Background(), routing.ModeCar, 35.6892, 51.389, 35.71, 51.41)
	if err != nil {
		t.Fatalf("Route() error: %v", err)
	}
	dep := resp.Primary.Instructions[0]
	// 500 m → "500 متر"
	if !strings.Contains(dep.Text, "500 متر") {
		t.Errorf("depart text does not contain distance '500 متر': %q", dep.Text)
	}
}

// TestRoute_UnsupportedMode returns an error for airplane.
func TestRoute_UnsupportedMode(t *testing.T) {
	c := New("http://127.0.0.1:1", 0) // unreachable on purpose
	_, err := c.Route(context.Background(), routing.ModeAirplane, 35, 51, 36, 52)
	if err == nil {
		t.Fatal("expected error for airplane mode")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("error should mention 'not supported': %v", err)
	}
}

// TestRoute_HTTPError propagates non-200 as an error.
func TestRoute_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := New(srv.URL, 0).Route(context.Background(), routing.ModeCar, 35, 51, 36, 52)
	if err == nil {
		t.Fatal("expected error for HTTP 503")
	}
}

// TestRoute_OSRMErrorCode surfaces OSRM-level errors.
func TestRoute_OSRMErrorCode(t *testing.T) {
	srv := serve(t, `{"code":"NoRoute","message":"no route"}`)
	defer srv.Close()

	_, err := New(srv.URL, 0).Route(context.Background(), routing.ModeCar, 35, 51, 36, 52)
	if err == nil {
		t.Fatal("expected error for OSRM code=NoRoute")
	}
}

// TestCleanStreetName_RemovesOSMClasses verifies road-class filtering.
func TestCleanStreetName_RemovesOSMClasses(t *testing.T) {
	cases := []struct{ input, want string }{
		{"Main Street", "Main Street"},
		{"primary_link", ""},
		{"motorway", ""},
		{"residential", ""},
		{"Footway", "Footway"},   // mixed-case real name — keep it
		{"footway", ""},          // exact OSM class — strip it
		{"", ""},
		{"  Tehran Ave  ", "Tehran Ave"},
	}
	for _, tc := range cases {
		got := cleanStreetName(tc.input)
		if got != tc.want {
			t.Errorf("cleanStreetName(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
