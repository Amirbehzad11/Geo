package model

// NearbyShipmentRequest is sent by a WebSocket client to search shipments near
// the passenger's current position.
type NearbyShipmentRequest struct {
	Type     string  `json:"type,omitempty"`
	Lat      float64 `json:"lat"`
	Lng      float64 `json:"lng"`
	RadiusKm float64 `json:"radius_km,omitempty"`
	Limit    int     `json:"limit,omitempty"`

	// FilterVehicleTypes, when non-empty, restricts results to shipments whose
	// vehicle_allowed list contains at least one vehicle whose ID is in the
	// list. Values are vehicle_types.id integers (e.g. [1, 2, 3, 4]).
	FilterVehicleTypes []int64 `json:"filter_vehicle_types,omitempty"`
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

// VehicleBoxSize is a legacy shape kept for backward compatibility.
type VehicleBoxSize struct {
	ID        any     // primary key of the vehicle_types row
	MaxWeight float64 // max weight this vehicle can carry (kg)
	Title     string  // human-readable title from vehicle_types.title
}

// VehicleType is one row from vehicle_types.
type VehicleType struct {
	ID    any    // primary key of the vehicle_types row
	Label string // human-readable label from vehicle_types.label
	Title string
}

// ShipmentVehicle is the per-vehicle object embedded in each shipment's
// "vehicles" array in the WebSocket response.
type ShipmentVehicle struct {
	VehicleTypeID any    `json:"id"`
	Label         string `json:"label"`
	Title         string `json:"title"`
}
