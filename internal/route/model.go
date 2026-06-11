package route

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

// RouteLeg is one segment of a multi-modal journey (e.g. walk, then train).
type RouteLeg struct {
	Mode         string             `json:"mode"`
	DistanceKm   float64            `json:"distance_km"`
	DurationMin  float64            `json:"duration_min"`
	Polyline     string             `json:"polyline"`
	Instructions []RouteInstruction `json:"instructions,omitempty"`
}

type RouteResponse struct {
	// Backward-compatible primary route fields.
	Distance float64 `json:"distance"` // km
	Duration float64 `json:"duration"` // minutes
	Polyline string  `json:"polyline"` // Google Encoded Polyline (combined for multi-modal)

	Mode    string        `json:"mode"`
	Primary RouteOption   `json:"primary"`
	Routes  []RouteOption `json:"-"`

	// Legs is populated for multi-modal routes (e.g. train mode).
	// Each leg has its own mode, polyline, and instructions.
	Legs []RouteLeg `json:"legs,omitempty"`
}

type RouteOption struct {
	ID           int                `json:"id"`
	Mode         string             `json:"mode"`
	IsPrimary    bool               `json:"is_primary"`
	DistanceKm   float64            `json:"distance_km"`
	DurationMin  float64            `json:"duration_min"`
	Polyline     string             `json:"polyline"`
	Instructions []RouteInstruction `json:"instructions,omitempty"`
}

type RouteInstruction struct {
	Index       int        `json:"index"`
	Type        string     `json:"type"`
	Modifier    string     `json:"modifier"`
	Text        string     `json:"text"`
	DistanceKm  float64    `json:"distance_km"`
	DurationMin float64    `json:"duration_min"`
	Location    RoutePoint `json:"location"`
	StreetName  string     `json:"street_name,omitempty"`
}

type RoutePoint struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// ── Multi-waypoint routing ────────────────────────────────────────────────────

// Waypoint is a single stop in a multi-waypoint route.
type Waypoint struct {
	Lat   float64 `json:"lat"`
	Lng   float64 `json:"lng"`
	Label string  `json:"label,omitempty"` // optional human-readable name
}

// MultiRouteRequest asks for a route through a waypoint list.
// The first waypoint is treated as the start, and the remaining waypoints are
// reordered greedily by nearest-neighbor (Haversine distance) before routing.
type MultiRouteRequest struct {
	Waypoints []Waypoint `json:"waypoints"` // at least 2; first is the start
	Mode      string     `json:"mode"`
	TripID    int64      `json:"trip_id,omitempty"`
}

// MultiRouteLeg is the computed route for one waypoint→waypoint segment.
type MultiRouteLeg struct {
	From        Waypoint `json:"from"`
	To          Waypoint `json:"to"`
	DistanceKm  float64  `json:"distance_km"`
	DurationMin float64  `json:"duration_min"`
}

// MultiRouteResponse is returned by POST /route/waypoints.
type MultiRouteResponse struct {
	TotalDistanceKm  float64         `json:"total_distance_km"`
	TotalDurationMin float64         `json:"total_duration_min"`
	Polyline         string          `json:"polyline"` // combined path
	Mode             string          `json:"mode"`
	Legs             []MultiRouteLeg `json:"legs"`
}
