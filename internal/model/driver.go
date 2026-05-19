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
	DriverID    StringID `json:"driver_id"`
	Lat         float64  `json:"lat"`
	Lng         float64  `json:"lng"`
	TimestampMs int64    `json:"timestamp_ms,omitempty"`
}

type DriverLocation struct {
	ID          string  `json:"id"`
	Lat         float64 `json:"lat"`
	Lng         float64 `json:"lng"`
	TimestampMs int64   `json:"timestamp_ms"`
	DistanceKm  float64 `json:"distance_km,omitempty"`
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
