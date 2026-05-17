package events

import (
	"encoding/json"
	"time"
)

// Type identifies the kind of event.
type Type string

const (
	LocationUpdated   Type = "location.updated"
	TripStarted       Type = "trip.started"
	TripEnded         Type = "trip.ended"
	RouteCalculated   Type = "route.calculated"
	DeviationDetected Type = "deviation.detected"
)

// Event is the envelope for all events on the bus.
type Event struct {
	Type      Type            `json:"type"`
	TripID    int64           `json:"trip_id"`
	Timestamp int64           `json:"timestamp_ms"` // Unix milliseconds
	Payload   json.RawMessage `json:"payload"`
}

// New constructs an Event, marshalling payload to JSON.
func New(t Type, tripID int64, payload any) (*Event, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &Event{
		Type:      t,
		TripID:    tripID,
		Timestamp: time.Now().UnixMilli(),
		Payload:   data,
	}, nil
}
