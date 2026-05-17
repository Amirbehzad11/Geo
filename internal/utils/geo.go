package utils

import "math"

const earthRadiusKm = 6371.0

// Haversine returns the great-circle distance in km between two coordinates.
func Haversine(lat1, lng1, lat2, lng2 float64) float64 {
	dLat := toRad(lat2 - lat1)
	dLng := toRad(lng2 - lng1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(toRad(lat1))*math.Cos(toRad(lat2))*
			math.Sin(dLng/2)*math.Sin(dLng/2)
	return earthRadiusKm * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// BearingRad returns the initial bearing from (lat1,lng1) to (lat2,lng2) in radians.
func BearingRad(lat1, lng1, lat2, lng2 float64) float64 {
	φ1 := toRad(lat1)
	φ2 := toRad(lat2)
	Δλ := toRad(lng2 - lng1)
	y := math.Sin(Δλ) * math.Cos(φ2)
	x := math.Cos(φ1)*math.Sin(φ2) - math.Sin(φ1)*math.Cos(φ2)*math.Cos(Δλ)
	return math.Atan2(y, x)
}

// CrossTrackDistance returns the perpendicular distance in km from point P
// to the segment A→B, and whether the foot of the perpendicular lies within
// the segment (rather than beyond one of the endpoints).
func CrossTrackDistance(pLat, pLng, aLat, aLng, bLat, bLng float64) (distKm float64, within bool) {
	dAP := Haversine(aLat, aLng, pLat, pLng)
	dAB := Haversine(aLat, aLng, bLat, bLng)
	if dAB == 0 {
		return dAP, false
	}

	θAP := BearingRad(aLat, aLng, pLat, pLng)
	θAB := BearingRad(aLat, aLng, bLat, bLng)

	sinXT := clamp(math.Sin(dAP/earthRadiusKm) * math.Sin(θAP-θAB))
	dXTRad := math.Asin(sinXT)
	dXT := math.Abs(dXTRad) * earthRadiusKm

	// Along-track distance: how far along A→B the foot lands.
	cosAT := clamp(math.Cos(dAP/earthRadiusKm) / math.Cos(dXTRad))
	dAT := math.Acos(cosAT) * earthRadiusKm
	within = dAT >= 0 && dAT <= dAB

	return dXT, within
}

// MinDistanceToPolyline returns the minimum distance in km from point (lat,lng)
// to any segment of the polyline. Used for route deviation detection.
type PolyPoint struct{ Lat, Lng float64 }

func MinDistanceToPolyline(lat, lng float64, pts []PolyPoint) float64 {
	if len(pts) == 0 {
		return math.MaxFloat64
	}
	if len(pts) == 1 {
		return Haversine(lat, lng, pts[0].Lat, pts[0].Lng)
	}

	minDist := math.MaxFloat64
	for i := 0; i < len(pts)-1; i++ {
		dXT, within := CrossTrackDistance(lat, lng, pts[i].Lat, pts[i].Lng, pts[i+1].Lat, pts[i+1].Lng)
		var d float64
		if within {
			d = dXT
		} else {
			d = math.Min(
				Haversine(lat, lng, pts[i].Lat, pts[i].Lng),
				Haversine(lat, lng, pts[i+1].Lat, pts[i+1].Lng),
			)
		}
		if d < minDist {
			minDist = d
		}
	}
	return minDist
}

// ValidCoords returns true when lat/lng are within legal bounds.
func ValidCoords(lat, lng float64) bool {
	return lat >= -90 && lat <= 90 && lng >= -180 && lng <= 180
}

// Round rounds v to precision decimal places.
func Round(v float64, precision int) float64 {
	ratio := math.Pow(10, float64(precision))
	return math.Round(v*ratio) / ratio
}

func toRad(deg float64) float64  { return deg * math.Pi / 180 }
func clamp(v float64) float64    { return math.Max(-1, math.Min(1, v)) }
