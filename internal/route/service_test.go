package route

import (
	"encoding/json"
	"testing"

	"geo-service/internal/routing"
)

func TestRouteResponseOmitsRoutesFromJSON(t *testing.T) {
	resp := buildRouteResponse(routing.ModeCar, []*routing.Route{{
		Distance: 1,
		Duration: 2,
		Polyline: "abc",
	}})

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal route response: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal route response: %v", err)
	}
	if _, ok := decoded["routes"]; ok {
		t.Fatalf("routes should not be present in JSON: %s", raw)
	}
	if _, ok := decoded["primary"]; !ok {
		t.Fatalf("primary should be present in JSON: %s", raw)
	}
	if len(resp.Routes) == 0 {
		t.Fatal("internal routes should remain available for persistence")
	}
}
