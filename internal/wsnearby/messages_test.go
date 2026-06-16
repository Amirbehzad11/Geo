package wsnearby_test

import (
	"encoding/json"
	"testing"

	"geo-service/internal/wsnearby"
)

func TestParseInboundSubscribeLocation(t *testing.T) {
	raw := []byte(`{
		"type": "SUBSCRIBE_LOCATION",
		"data": {
			"lat": 32.64,
			"lng": 51.66,
			"role": "passenger",
			"radius_km": 2,
			"limit": 50
		}
	}`)
	msg, req, ok, err := wsnearby.ParseInbound(raw, false)
	if err != nil || !ok {
		t.Fatalf("unexpected error: %v ok=%v", err, ok)
	}
	if msg.Type != wsnearby.MsgSubscribeLocation {
		t.Fatalf("type=%q", msg.Type)
	}
	if req.Lat != 32.64 || req.Lng != 51.66 || req.Type != "passenger" {
		t.Fatalf("req=%+v", req)
	}
}

func TestParseInboundRejectsUnknownType(t *testing.T) {
	_, _, _, err := wsnearby.ParseInbound([]byte(`{"type":"FOO","data":{}}`), false)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseInboundLegacy(t *testing.T) {
	raw := []byte(`{"type":"passenger","lat":32.1,"lng":51.2}`)
	_, req, ok, err := wsnearby.ParseInbound(raw, true)
	if err != nil || !ok {
		t.Fatalf("err=%v ok=%v", err, ok)
	}
	if req.Type != "passenger" {
		t.Fatalf("type=%q", req.Type)
	}
}

func TestParseInboundPing(t *testing.T) {
	msg, _, ok, err := wsnearby.ParseInbound([]byte(`{"type":"PING"}`), false)
	if err != nil || ok {
		t.Fatalf("err=%v ok=%v", err, ok)
	}
	if msg.Type != wsnearby.MsgPing {
		t.Fatalf("type=%q", msg.Type)
	}
}

func TestParseInboundInvalidCoords(t *testing.T) {
	data, _ := json.Marshal(map[string]any{
		"type": "SUBSCRIBE_LOCATION",
		"data": map[string]any{"lat": 120, "lng": 51},
	})
	_, _, _, err := wsnearby.ParseInbound(data, false)
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestMessageLimiterBurst(t *testing.T) {
	lim := wsnearby.NewMessageLimiter(1, 2)
	if !lim.Allow() || !lim.Allow() {
		t.Fatal("expected burst of 2")
	}
	if lim.Allow() {
		t.Fatal("expected third message blocked")
	}
}
