package observability

import (
	"geo-service/internal/wsplatform"
)

// Shipment metrics delegate to the unified wsplatform Prometheus registry.
// Kept for backward compatibility with existing dashboards/alerts.

func ShipmentConnectionOpened(clientIP string) {
	wsplatform.ConnectionOpened(wsplatform.ChannelShipmentNearby, clientIP)
}

func ShipmentConnectionClosed(clientIP, reason string) {
	wsplatform.ConnectionClosed(wsplatform.ChannelShipmentNearby, clientIP, reason)
}

func ShipmentMessageInbound() {
	wsplatform.MessageInbound(wsplatform.ChannelShipmentNearby)
}

func ShipmentMessageOutbound() {
	wsplatform.MessageOutbound(wsplatform.ChannelShipmentNearby)
}
