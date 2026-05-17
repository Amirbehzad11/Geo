package utils

// SmoothGPS applies an exponential moving average to GPS coordinates.
// alpha controls how much weight the new observation receives (0 < alpha ≤ 1).
// alpha = 1.0 → no smoothing (pass-through).
// alpha = 0.7 → 70 % new observation, 30 % previous (good default for 3-5s intervals).
//
// Returns the raw coordinates unchanged when no previous position is available
// (prevLat == 0 && prevLng == 0 treated as "no history").
func SmoothGPS(rawLat, rawLng, prevLat, prevLng, alpha float64) (lat, lng float64) {
	if prevLat == 0 && prevLng == 0 {
		return rawLat, rawLng
	}
	return alpha*rawLat + (1-alpha)*prevLat,
		alpha*rawLng + (1-alpha)*prevLng
}
