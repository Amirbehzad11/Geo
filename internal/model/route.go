package model

type RouteRequest struct {
	TripID       int64   `json:"trip_id"` // optional
	StartLat     float64 `json:"start_lat"`
	StartLng     float64 `json:"start_lng"`
	EndLat       float64 `json:"end_lat"`
	EndLng       float64 `json:"end_lng"`
	Mode         string  `json:"mode"`         // car | motorcycle | bus | walking | airplane (default: car)
	VehicleType  string  `json:"vehicle_type"` // alias for mode, useful for Laravel payloads
	Alternatives int     `json:"alternatives"` // requested route count, clamped server-side
}

type RouteResponse struct {
	// Backward-compatible primary route fields.
	Distance float64 `json:"distance"` // km
	Duration float64 `json:"duration"` // minutes
	Polyline string  `json:"polyline"` // Google Encoded Polyline

	Mode    string        `json:"mode"`
	Primary RouteOption   `json:"primary"`
	Routes  []RouteOption `json:"routes"`
}

type RouteOption struct {
	ID          int         `json:"id"`
	Mode        string      `json:"mode"`
	IsPrimary   bool        `json:"is_primary"`
	DistanceKm  float64     `json:"distance_km"`
	DurationMin float64     `json:"duration_min"`
	Polyline    string      `json:"polyline"`
	// Path contains the full 3-D route geometry as [lat, lng, altMetres] tuples.
	// For ground routes altMetres is 0; for airplane mode it follows a sinusoidal
	// profile peaking at ~11 000 m (35 000 ft) at the midpoint.
	Path        [][3]float64 `json:"path"`
}
