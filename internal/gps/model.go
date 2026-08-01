package gps

type GPSUpdate struct {
	// TripID is optional. When set, the update is tracked against that trip.
	// When omitted, the Bearer user's live map presence is updated instead.
	TripID    int64   `json:"trip_id,omitempty"`
	Lat       float64 `json:"lat"`
	Lng       float64 `json:"lng"`
	Timestamp int64   `json:"timestamp,omitempty"` // Unix seconds; optional for presence updates
}

type LocationState struct {
	TripID      int64   `json:"trip_id"`
	Lat         float64 `json:"lat"`
	Lng         float64 `json:"lng"`
	SpeedKmH    float64 `json:"speed_kmh"`
	Timestamp   int64   `json:"timestamp"`
	DeviationKm float64 `json:"deviation_km,omitempty"` // distance from planned route
}
