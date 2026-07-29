package model

import (
	"encoding/json"
	"strings"
)

// StringID accepts either a JSON string or number and keeps the value as a
// stable Redis member id.
type StringID string

func (s *StringID) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		*s = ""
		return nil
	}

	if strings.HasPrefix(raw, `"`) {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		*s = StringID(strings.TrimSpace(value))
		return nil
	}

	*s = StringID(raw)
	return nil
}

func (s StringID) String() string {
	return string(s)
}

type DriverLocationRequest struct {
	// DriverID is required for API-key clients. JWT Bearer clients ignore this
	// field; the authenticated user_id/sub from the token is used instead.
	DriverID    StringID `json:"driver_id,omitempty"`
	Lat         float64  `json:"lat"`
	Lng         float64  `json:"lng"`
	TimestampMs int64    `json:"timestamp_ms,omitempty"`
}

// DriverActiveJob is the latest in-progress shipping for a nearby driver.
// When HasShipping is true, Destination/TripID/VehicleTypeID come from the
// active shipping row. When HasShipping is false, only LatestTrip is set
// (last registered trip with no active shipping).
type DriverActiveJob struct {
	// Shipping-based fields (populated when HasShipping = true)
	Destination      string `json:"destination,omitempty"`
	TripID           int64  `json:"trip_id,omitempty"`
	VehicleTypeID    int64  `json:"vehicle_type_id,omitempty"`
	VehicleTypeImage string `json:"vehicle_type_image,omitempty"`

	// HasShipping indicates whether the driver has an active shipping.
	HasShipping bool `json:"has_shipping"`

	// LatestTrip is the last trip registered by the driver when there is no
	// active shipping. Nil when HasShipping is true.
	LatestTrip *DriverLatestTrip `json:"latest_trip,omitempty"`
}

// DriverLatestTrip carries the minimal trip info shown on the sender map
// when the driver has no active shipping.
type DriverLatestTrip struct {
	TripID      int64  `json:"trip_id"`
	Destination string `json:"destination"`
}

type DriverLocation struct {
	ID          string           `json:"id"`
	DriverID    int64            `json:"driver_id,omitempty"`
	Lat         float64          `json:"lat"`
	Lng         float64          `json:"lng"`
	TimestampMs int64            `json:"timestamp_ms"`
	DistanceKm  float64          `json:"distance_km,omitempty"`
	Active      *DriverActiveJob `json:"active"`
}

type DriverLocationResponse struct {
	Type      string         `json:"type"`
	Timestamp int64          `json:"timestamp_ms"`
	Driver    DriverLocation `json:"driver"`
}

type NearbyDriverQuery struct {
	Lat      float64 `json:"lat"`
	Lng      float64 `json:"lng"`
	RadiusKm float64 `json:"radius_km"`
	Limit    int     `json:"limit"`
}

type NearbyDriverResponse struct {
	Type      string            `json:"type"`
	Timestamp int64             `json:"timestamp_ms"`
	Query     NearbyDriverQuery `json:"query"`
	Count     int               `json:"count"`
	Drivers   []DriverLocation  `json:"drivers"`
}
