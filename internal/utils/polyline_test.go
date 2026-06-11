package utils

import (
	"testing"
)

func TestEncodeDecodeRoundtrip(t *testing.T) {
	pts := []PolyPoint{
		{Lat: 35.71261, Lng: 51.40441}, // Tehran
		{Lat: 36.29807, Lng: 59.60567}, // Mashhad
		{Lat: 32.66546, Lng: 51.66768}, // Isfahan
	}

	encoded := EncodePolyline(pts)
	if encoded == "" {
		t.Fatal("encoded polyline is empty")
	}

	decoded := DecodePolyline(encoded)
	if len(decoded) != len(pts) {
		t.Fatalf("expected %d points, got %d", len(pts), len(decoded))
	}

	for i, want := range pts {
		got := decoded[i]
		// Allow 1e-5 precision loss (inherent in the encoding)
		if abs(got.Lat-want.Lat) > 1e-4 || abs(got.Lng-want.Lng) > 1e-4 {
			t.Errorf("point[%d]: want (%.5f,%.5f), got (%.5f,%.5f)",
				i, want.Lat, want.Lng, got.Lat, got.Lng)
		}
	}
}

func TestCombinePolylines(t *testing.T) {
	seg1 := []PolyPoint{{Lat: 32.0, Lng: 51.0}, {Lat: 33.0, Lng: 51.5}, {Lat: 34.0, Lng: 51.8}}
	seg2 := []PolyPoint{{Lat: 34.0, Lng: 51.8}, {Lat: 35.0, Lng: 51.4}, {Lat: 35.7, Lng: 51.4}}

	enc1 := EncodePolyline(seg1)
	enc2 := EncodePolyline(seg2)

	combined := CombinePolylines([]string{enc1, enc2})
	pts := DecodePolyline(combined)

	// 3 points + 2 points - 1 duplicate = 5 points
	if len(pts) != 5 {
		t.Fatalf("expected 5 combined points (duplicate junction removed), got %d", len(pts))
	}
}

func TestDecodeEmpty(t *testing.T) {
	if pts := DecodePolyline(""); pts != nil {
		t.Fatalf("expected nil for empty input, got %v", pts)
	}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
