package model

// NearbyShipmentRequest is sent by a WebSocket client to search shipments near
// the passenger's current position.
type NearbyShipmentRequest struct {
	Type     string  `json:"type,omitempty"`
	Lat      float64 `json:"lat"`
	Lng      float64 `json:"lng"`
	RadiusKm float64 `json:"radius_km,omitempty"`
	Limit    int     `json:"limit,omitempty"`
}

// NearbyShipmentQuery echoes the normalized query used by the service.
type NearbyShipmentQuery struct {
	Lat      float64 `json:"lat"`
	Lng      float64 `json:"lng"`
	RadiusKm float64 `json:"radius_km"`
	Limit    int     `json:"limit"`
}

// NearbyShipmentResponse is sent over WebSocket after each nearby shipment
// search. Shipment rows are returned as dynamic maps because the Laravel table
// schema is owned by another application.
type NearbyShipmentResponse struct {
	Type      string              `json:"type"`
	Timestamp int64               `json:"timestamp_ms"`
	Query     NearbyShipmentQuery `json:"query"`
	Count     int                 `json:"count"`
	Shipments []map[string]any    `json:"shipments"`
}
