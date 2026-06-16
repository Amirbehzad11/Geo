package wsnearby

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"geo-service/internal/model"
	"geo-service/internal/utils"
)

const (
	MsgSubscribeLocation = "SUBSCRIBE_LOCATION"
	MsgPing              = "PING"
	MsgPong              = "PONG"
)

// InboundMessage is the strict WebSocket envelope.
type InboundMessage struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// SubscribeData is the payload for SUBSCRIBE_LOCATION.
type SubscribeData struct {
	Lat                float64 `json:"lat"`
	Lng                float64 `json:"lng"`
	Role               string  `json:"role,omitempty"`
	RadiusKm           float64 `json:"radius_km,omitempty"`
	Limit              int     `json:"limit,omitempty"`
	FilterVehicleTypes []int64 `json:"filter_vehicle_types,omitempty"`
}

// ParseInbound decodes and validates a client message.
func ParseInbound(raw []byte, allowLegacy bool) (InboundMessage, model.NearbyShipmentRequest, bool, error) {
	raw = trimJSON(raw)
	if len(raw) == 0 {
		return InboundMessage{}, model.NearbyShipmentRequest{}, false, errors.New("empty message")
	}

	var env InboundMessage
	if err := json.Unmarshal(raw, &env); err != nil {
		if allowLegacy {
			req, err := tryLegacyNearbyRequest(raw)
			if err != nil {
				return InboundMessage{}, model.NearbyShipmentRequest{}, false, errors.New("message must be valid JSON")
			}
			return InboundMessage{Type: "LEGACY"}, req, true, nil
		}
		return InboundMessage{}, model.NearbyShipmentRequest{}, false, errors.New("message must be valid JSON")
	}

	msgType := strings.ToUpper(strings.TrimSpace(env.Type))
	switch msgType {
	case MsgPing:
		return InboundMessage{Type: MsgPing}, model.NearbyShipmentRequest{}, false, nil
	case MsgSubscribeLocation:
		if len(env.Data) == 0 {
			return InboundMessage{}, model.NearbyShipmentRequest{}, false, errors.New("SUBSCRIBE_LOCATION requires data")
		}
		var data SubscribeData
		if err := json.Unmarshal(env.Data, &data); err != nil {
			return InboundMessage{}, model.NearbyShipmentRequest{}, false, errors.New("SUBSCRIBE_LOCATION data is invalid")
		}
		req, err := subscribeDataToRequest(data)
		if err != nil {
			return InboundMessage{}, model.NearbyShipmentRequest{}, false, err
		}
		return InboundMessage{Type: MsgSubscribeLocation, Data: env.Data}, req, true, nil
	default:
		if allowLegacy {
			if req, err := tryLegacyNearbyRequest(raw); err == nil {
				return InboundMessage{Type: "LEGACY"}, req, true, nil
			}
		}
		return InboundMessage{}, model.NearbyShipmentRequest{}, false, fmt.Errorf("unsupported message type %q", env.Type)
	}
}

func tryLegacyNearbyRequest(raw []byte) (model.NearbyShipmentRequest, error) {
	var req model.NearbyShipmentRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return model.NearbyShipmentRequest{}, err
	}
	if err := validateNearbyRequest(req); err != nil {
		return model.NearbyShipmentRequest{}, err
	}
	return req, nil
}

func subscribeDataToRequest(data SubscribeData) (model.NearbyShipmentRequest, error) {
	role := strings.ToLower(strings.TrimSpace(data.Role))
	reqType := "passenger"
	switch role {
	case "", "passenger", "shipment.nearby":
		reqType = "passenger"
	case "sender":
		reqType = "sender"
	default:
		return model.NearbyShipmentRequest{}, fmt.Errorf("unsupported role %q", data.Role)
	}
	req := model.NearbyShipmentRequest{
		Type:               reqType,
		Lat:                data.Lat,
		Lng:                data.Lng,
		RadiusKm:           data.RadiusKm,
		Limit:              data.Limit,
		FilterVehicleTypes: data.FilterVehicleTypes,
	}
	return req, validateNearbyRequest(req)
}

func validateNearbyRequest(req model.NearbyShipmentRequest) error {
	if !utils.ValidCoords(req.Lat, req.Lng) {
		return errors.New("coordinates out of valid range (-90 <= lat <= 90, -180 <= lng <= 180)")
	}
	if req.RadiusKm < 0 {
		return errors.New("radius_km must be positive")
	}
	if req.Limit < 0 {
		return errors.New("limit must be positive")
	}
	if nearbyRequestType(req.Type) == "" {
		return errors.New("unsupported nearby request type")
	}
	return nil
}

func nearbyRequestType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "passenger", "shipment.nearby":
		return "passenger"
	case "sender":
		return "sender"
	default:
		return ""
	}
}

func trimJSON(raw []byte) []byte {
	return []byte(strings.TrimSpace(string(raw)))
}
