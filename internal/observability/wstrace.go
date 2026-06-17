package observability

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const shipmentTracerName = "geo-service/ws-shipment"

const shipmentChannel = "shipment.nearby"

// ShipmentTracer returns the tracer for shipment nearby WebSocket spans.
func ShipmentTracer() trace.Tracer {
	return otel.Tracer(shipmentTracerName)
}

// StartShipmentConnectSpan traces the WebSocket handshake lifecycle.
func StartShipmentConnectSpan(ctx context.Context, connectionID, clientIP string, userID int64) (context.Context, trace.Span) {
	attrs := []attribute.KeyValue{
		attribute.String("ws.channel", shipmentChannel),
		attribute.String("ws.connection_id", connectionID),
		attribute.String("client.ip", clientIP),
	}
	if userID > 0 {
		attrs = append(attrs, attribute.Int64("user.id", userID))
	}
	return ShipmentTracer().Start(ctx, "ws.shipment.connect", trace.WithAttributes(attrs...))
}

// StartShipmentMessageSpan traces inbound message handling.
func StartShipmentMessageSpan(ctx context.Context, connectionID, messageType string) (context.Context, trace.Span) {
	return ShipmentTracer().Start(ctx, "ws.shipment.message",
		trace.WithAttributes(
			attribute.String("ws.channel", shipmentChannel),
			attribute.String("ws.connection_id", connectionID),
			attribute.String("ws.message_type", messageType),
		),
	)
}

// StartShipmentSearchSpan traces nearby search backend calls.
func StartShipmentSearchSpan(ctx context.Context, connectionID, searchType string) (context.Context, trace.Span) {
	return ShipmentTracer().Start(ctx, "ws.shipment.search",
		trace.WithAttributes(
			attribute.String("ws.channel", shipmentChannel),
			attribute.String("ws.connection_id", connectionID),
			attribute.String("search.type", searchType),
		),
	)
}

// RecordSpanError marks a span as failed.
func RecordSpanError(span trace.Span, err error) {
	if span == nil || err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}
