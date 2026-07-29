package service

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"geo-service/internal/model"
)

type fakeShipmentRepo struct {
	rows  []map[string]any
	types []model.VehicleType

	shippings map[int64]map[string]any
}

func (f fakeShipmentRepo) FindNearbyShipments(context.Context, float64, float64, float64, int) ([]map[string]any, error) {
	return f.rows, nil
}

func (f fakeShipmentRepo) LoadVehicleTypes(context.Context) ([]model.VehicleType, error) {
	return f.types, nil
}

func (f fakeShipmentRepo) LoadShippingsByShipmentIDs(ctx context.Context, shipmentIDs []int64) (map[int64]map[string]any, error) {
	if len(shipmentIDs) == 0 {
		return map[int64]map[string]any{}, nil
	}
	out := make(map[int64]map[string]any, len(shipmentIDs))
	for _, id := range shipmentIDs {
		if f.shippings == nil {
			continue
		}
		if s, ok := f.shippings[id]; ok {
			out[id] = s
		}
	}
	return out, nil
}

func TestShipmentServiceSearchNearbyUsesVehicleAllowed(t *testing.T) {
	repo := fakeShipmentRepo{
		rows: []map[string]any{
			{"id": 1, "vehicle_allowed": "[1,2]"},
			{"id": 2, "vehicle_allowed": "{3,4}"},
		},
		types: []model.VehicleType{
			{ID: int64(1), Label: "bike", Title: "Bike"},
			{ID: int64(2), Label: "van", Title: "Van"},
			{ID: int64(3), Label: "pickup", Title: "Pickup"},
			{ID: int64(4), Label: "truck", Title: "Truck"},
		},
	}

	svc := NewShipmentService(repo, 7, 13)
	resp, err := svc.SearchNearby(context.Background(), model.NearbyShipmentRequest{
		Lat:                35.7,
		Lng:                51.4,
		FilterVehicleTypes: []int64{4},
	}, 0)
	if err != nil {
		t.Fatalf("SearchNearby returned error: %v", err)
	}
	if resp.Query.RadiusKm != 7 {
		t.Fatalf("expected default radius 7, got %v", resp.Query.RadiusKm)
	}
	if resp.Query.Limit != 13 {
		t.Fatalf("expected default limit 13, got %d", resp.Query.Limit)
	}
	if resp.Count != 1 || len(resp.Shipments) != 1 {
		t.Fatalf("expected 1 shipment after filtering, got count=%d len=%d", resp.Count, len(resp.Shipments))
	}

	row := resp.Shipments[0]
	if got := row["id"]; got != 2 {
		t.Fatalf("expected shipment 2 to survive filter, got %#v", got)
	}

	vehicles, ok := row["vehicles"].([]model.ShipmentVehicle)
	if !ok {
		t.Fatalf("expected vehicles to be []model.ShipmentVehicle, got %T", row["vehicles"])
	}
	want := []model.ShipmentVehicle{
		{VehicleTypeID: int64(3), Label: "pickup", Title: "Pickup"},
		{VehicleTypeID: int64(4), Label: "truck", Title: "Truck"},
	}
	if !reflect.DeepEqual(vehicles, want) {
		t.Fatalf("unexpected vehicles: got %#v want %#v", vehicles, want)
	}
}

func TestShipmentAllowedVehicleIDsParsesCommonFormats(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want []int64
	}{
		{name: "json", in: "[1,2]", want: []int64{1, 2}},
		{name: "postgres", in: "{3,4}", want: []int64{3, 4}},
		{name: "comma", in: "5, 6", want: []int64{5, 6}},
		{name: "bytes", in: []byte("[7,8]"), want: []int64{7, 8}},
		{name: "number", in: json.Number("9"), want: []int64{9}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shipmentAllowedVehicleIDs(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("unexpected ids: got %#v want %#v", got, tc.want)
			}
		})
	}
}

func TestShipmentServiceHidesShipmentsWithActiveShippingForOtherPassengers(t *testing.T) {
	repo := fakeShipmentRepo{
		rows: []map[string]any{
			{"id": int64(1), "vehicle_allowed": nil},
			{"id": int64(2), "vehicle_allowed": nil},
		},
		types:      nil,
		shippings: map[int64]map[string]any{1: {"passenger_user_id": int64(10)}, 2: {"passenger_user_id": int64(11)}},
	}

	svc := NewShipmentService(repo, 0, 10)
	resp, err := svc.SearchNearby(context.Background(), model.NearbyShipmentRequest{
		Lat: 32.0,
		Lng: 51.0,
	}, 10)
	if err != nil {
		t.Fatalf("SearchNearby returned error: %v", err)
	}
	if resp.Count != 1 || len(resp.Shipments) != 1 {
		t.Fatalf("expected 1 shipment after filtering, got count=%d len=%d", resp.Count, len(resp.Shipments))
	}
	if got := resp.Shipments[0]["id"]; got != int64(1) {
		t.Fatalf("expected shipment id=1 to survive, got %#v", got)
	}
}
