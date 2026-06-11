package utils

import (
	"math"
	"strings"
)

// EncodePolyline encodes a slice of PolyPoint into a Google Encoded Polyline
// string (precision 1e-5).
func EncodePolyline(pts []PolyPoint) string {
	var sb strings.Builder
	prevLat, prevLng := 0, 0
	for _, p := range pts {
		lat := int(math.Round(p.Lat * 1e5))
		lng := int(math.Round(p.Lng * 1e5))
		encodeChunk(&sb, lat-prevLat)
		encodeChunk(&sb, lng-prevLng)
		prevLat, prevLng = lat, lng
	}
	return sb.String()
}

// DecodePolyline decodes a Google Encoded Polyline string into a slice of
// PolyPoint. Returns nil on empty input.
func DecodePolyline(encoded string) []PolyPoint {
	if encoded == "" {
		return nil
	}
	var pts []PolyPoint
	idx, lat, lng := 0, 0, 0
	n := len(encoded)
	for idx < n {
		dlat, i := decodeChunk(encoded, idx)
		idx = i
		lat += dlat

		dlng, i := decodeChunk(encoded, idx)
		idx = i
		lng += dlng

		pts = append(pts, PolyPoint{
			Lat: float64(lat) / 1e5,
			Lng: float64(lng) / 1e5,
		})
	}
	return pts
}

// encodeChunk encodes a single signed integer (delta) as a variable-length
// base-64 sequence per the Google Encoded Polyline spec.
func encodeChunk(sb *strings.Builder, v int) {
	v <<= 1
	if v < 0 {
		v = ^v
	}
	for v >= 0x20 {
		sb.WriteByte(byte((0x20|(v&0x1f)) + 63))
		v >>= 5
	}
	sb.WriteByte(byte(v + 63))
}

// decodeChunk decodes one signed integer from encoded[idx:] and returns the
// value and the next index to read from.
func decodeChunk(encoded string, idx int) (int, int) {
	var result, shift int
	for {
		b := int(encoded[idx]) - 63
		idx++
		result |= (b & 0x1f) << shift
		shift += 5
		if b < 0x20 {
			break
		}
	}
	if result&1 != 0 {
		result = ^result
	}
	return result >> 1, idx
}

// CombinePolylines decodes each encoded polyline, concatenates the point
// slices (skipping the first point of each subsequent leg to avoid
// duplicates at junctions), and re-encodes the combined path.
// Returns an empty string if all inputs are empty.
func CombinePolylines(encodedLegs []string) string {
	var all []PolyPoint
	for i, enc := range encodedLegs {
		pts := DecodePolyline(enc)
		if len(pts) == 0 {
			continue
		}
		if i > 0 && len(all) > 0 {
			// Skip the first point of subsequent legs — it duplicates the
			// last point of the previous leg.
			pts = pts[1:]
		}
		all = append(all, pts...)
	}
	return EncodePolyline(all)
}
