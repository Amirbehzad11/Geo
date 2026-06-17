package wsnearby

import (
	"context"
	"errors"
	"fmt"

	"geo-service/internal/model"
	"geo-service/internal/service"
	"geo-service/internal/wsplatform"
)

var (
	ErrDriverDisabled  = service.ErrDriverLocationDisabled
	ErrShipmentDisabled = service.ErrShipmentSearchDisabled
)

// NearbyProcessor executes nearby searches behind circuit breakers and location guards.
type NearbyProcessor struct {
	shipments *service.ShipmentService
	drivers   *service.DriverService
	locations *wsplatform.LocationGuard
	shipmentCB *wsplatform.Breaker
	driverCB   *wsplatform.Breaker
	channel    string
}

func NewNearbyProcessor(
	shipments *service.ShipmentService,
	drivers *service.DriverService,
	locations *wsplatform.LocationGuard,
	shipmentCB, driverCB *wsplatform.Breaker,
) *NearbyProcessor {
	return &NearbyProcessor{
		shipments:  shipments,
		drivers:    drivers,
		locations:  locations,
		shipmentCB: shipmentCB,
		driverCB:   driverCB,
		channel:    wsplatform.ChannelShipmentNearby,
	}
}

// ProcessLocationUpdate validates movement and runs the appropriate nearby search.
func (p *NearbyProcessor) ProcessLocationUpdate(ctx context.Context, session *Session, req model.NearbyShipmentRequest) (any, error) {
	subject := session.ConnectionID()
	if session.UserID() > 0 {
		subject = fmt.Sprintf("u:%d", session.UserID())
	}
	if err := p.locations.Validate(ctx, subject, p.channel, req.Lat, req.Lng); err != nil {
		wsplatform.RecordError(p.channel, "location_rejected")
		return nil, err
	}

	switch nearbyRole(req.Type) {
	case "sender":
		return p.searchDrivers(ctx, req)
	case "passenger":
		return p.searchShipments(ctx, req)
	default:
		wsplatform.RecordError(p.channel, "validation_error")
		return nil, errors.New("unsupported nearby request type")
	}
}

func (p *NearbyProcessor) searchDrivers(ctx context.Context, req model.NearbyShipmentRequest) (any, error) {
	if p.drivers == nil {
		wsplatform.RecordError(p.channel, "driver_location_disabled")
		return nil, ErrDriverDisabled
	}
	var result any
	err := p.driverCB.Execute(func() (any, error) {
		resp, err := p.drivers.SearchNearby(ctx, req.Lat, req.Lng, req.RadiusKm, req.Limit)
		if err != nil {
			return nil, err
		}
		result = resp
		return resp, nil
	})
	if errors.Is(err, wsplatform.ErrCircuitOpen) {
		wsplatform.RecordError(p.channel, "circuit_open")
		return nil, err
	}
	if err != nil {
		if errors.Is(err, ErrDriverDisabled) {
			wsplatform.RecordError(p.channel, "driver_location_disabled")
			return nil, err
		}
		wsplatform.RecordError(p.channel, "driver_search_failed")
		return nil, err
	}
	return result, nil
}

func (p *NearbyProcessor) searchShipments(ctx context.Context, req model.NearbyShipmentRequest) (any, error) {
	if p.shipments == nil {
		wsplatform.RecordError(p.channel, "shipment_search_disabled")
		return nil, ErrShipmentDisabled
	}
	var result any
	err := p.shipmentCB.Execute(func() (any, error) {
		resp, err := p.shipments.SearchNearby(ctx, req)
		if err != nil {
			return nil, err
		}
		result = resp
		return resp, nil
	})
	if errors.Is(err, wsplatform.ErrCircuitOpen) {
		wsplatform.RecordError(p.channel, "circuit_open")
		return nil, err
	}
	if err != nil {
		if errors.Is(err, ErrShipmentDisabled) {
			wsplatform.RecordError(p.channel, "shipment_search_disabled")
			return nil, err
		}
		wsplatform.RecordError(p.channel, "shipment_search_failed")
		return nil, err
	}
	return result, nil
}

func nearbyRole(raw string) string {
	switch raw {
	case "sender":
		return "sender"
	case "", "passenger", "shipment.nearby":
		return "passenger"
	default:
		return ""
	}
}
