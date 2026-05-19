package service

import (
	"context"
	"errors"
	"time"

	"geo-service/internal/model"
)

var ErrShipmentSearchDisabled = errors.New("shipment search database is not configured")

const (
	defaultShipmentRadiusKm = 10.0
	defaultShipmentLimit    = 100
	maxShipmentLimit        = 500
)

// ShipmentRepository is the read-only storage contract used by ShipmentService.
type ShipmentRepository interface {
	FindNearbyShipments(ctx context.Context, lat, lng, radiusKm float64, limit int) ([]map[string]any, error)
}

// ShipmentService normalizes nearby shipment search requests before querying
// the Laravel-owned database.
type ShipmentService struct {
	repo            ShipmentRepository
	defaultRadiusKm float64
	defaultLimit    int
}

// NewShipmentService creates a service for passenger-nearby shipment lookup.
func NewShipmentService(repo ShipmentRepository, defaultRadiusKm float64, defaultLimit int) *ShipmentService {
	if defaultRadiusKm <= 0 {
		defaultRadiusKm = defaultShipmentRadiusKm
	}
	if defaultLimit <= 0 {
		defaultLimit = defaultShipmentLimit
	}
	if defaultLimit > maxShipmentLimit {
		defaultLimit = maxShipmentLimit
	}

	return &ShipmentService{
		repo:            repo,
		defaultRadiusKm: defaultRadiusKm,
		defaultLimit:    defaultLimit,
	}
}

// SearchNearby returns shipment rows whose origin coordinates are inside the
// requested radius from the passenger coordinate.
func (s *ShipmentService) SearchNearby(ctx context.Context, req model.NearbyShipmentRequest) (*model.NearbyShipmentResponse, error) {
	if s == nil || s.repo == nil {
		return nil, ErrShipmentSearchDisabled
	}

	radiusKm := req.RadiusKm
	if radiusKm <= 0 {
		radiusKm = s.defaultRadiusKm
	}

	limit := req.Limit
	if limit <= 0 {
		limit = s.defaultLimit
	}
	if limit > maxShipmentLimit {
		limit = maxShipmentLimit
	}

	rows, err := s.repo.FindNearbyShipments(ctx, req.Lat, req.Lng, radiusKm, limit)
	if err != nil {
		return nil, err
	}

	return &model.NearbyShipmentResponse{
		Type:      "shipment.nearby",
		Timestamp: time.Now().UnixMilli(),
		Query: model.NearbyShipmentQuery{
			Lat:      req.Lat,
			Lng:      req.Lng,
			RadiusKm: radiusKm,
			Limit:    limit,
		},
		Count:     len(rows),
		Shipments: rows,
	}, nil
}
