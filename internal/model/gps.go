package model

type GPSUpdate struct {
	TripID    int64   `json:"trip_id"`
	Lat       float64 `json:"lat"`
	Lng       float64 `json:"lng"`
	Timestamp int64   `json:"timestamp"` // Unix seconds
}

type LocationState struct {
	TripID      int64   `json:"trip_id"`
	Lat         float64 `json:"lat"`
	Lng         float64 `json:"lng"`
	SpeedKmH    float64 `json:"speed_kmh"`
	Timestamp   int64   `json:"timestamp"`
	DeviationKm float64 `json:"deviation_km,omitempty"` // distance from planned route
}
