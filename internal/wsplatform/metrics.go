package wsplatform

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	ChannelShipmentNearby = "shipment.nearby"
	ChannelTrip           = "trip"
)

var (
	activeConnections = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "geo_ws_active_connections",
		Help: "Currently active WebSocket connections (ws_active_connections).",
	}, []string{"channel"})

	connectionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "geo_ws_connections_total",
		Help: "WebSocket connection lifecycle events (ws_connections_total).",
	}, []string{"channel", "event"})

	messagesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "geo_ws_messages_total",
		Help: "WebSocket messages processed (ws_messages_total).",
	}, []string{"channel", "direction"})

	messageLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "geo_ws_message_duration_seconds",
		Help:    "WebSocket message handling latency; use *1000 for ws_message_latency_ms.",
		Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	}, []string{"channel"})

	authFailuresTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "geo_ws_auth_failures_total",
		Help: "WebSocket authentication failures (ws_auth_failures_total).",
	}, []string{"channel"})

	rateLimitedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "geo_ws_rate_limited_total",
		Help: "WebSocket rate-limit events (ws_rate_limited_total).",
	}, []string{"channel", "scope"})

	disconnectsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "geo_ws_disconnects_total",
		Help: "WebSocket disconnections (ws_disconnects_total).",
	}, []string{"channel", "reason"})

	errorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "geo_ws_errors_total",
		Help: "WebSocket errors (ws_error_total).",
	}, []string{"channel", "code"})

	connectionsPerIP = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "geo_ws_connections_per_ip",
		Help: "Active WebSocket connections per client IP (abuse indicator).",
	}, []string{"channel", "client_ip"})

	locationRejectedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "geo_ws_location_rejected_total",
		Help: "Rejected location updates (spoofing / unrealistic jumps).",
	}, []string{"channel", "reason"})

	backpressureTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "geo_ws_backpressure_total",
		Help: "Connections closed due to slow consumer backpressure.",
	}, []string{"channel"})

	broadcastDroppedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "geo_ws_broadcast_dropped_total",
		Help: "Broadcast messages dropped for slow clients.",
	}, []string{"channel"})

	circuitOpenTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "geo_ws_circuit_open_total",
		Help: "Downstream calls rejected because circuit breaker is open.",
	}, []string{"dependency"})
)

func ConnectionOpened(channel, clientIP string) {
	activeConnections.WithLabelValues(channel).Inc()
	connectionsPerIP.WithLabelValues(channel, clientIP).Inc()
	connectionsTotal.WithLabelValues(channel, "opened").Inc()
}

func ConnectionClosed(channel, clientIP, reason string) {
	activeConnections.WithLabelValues(channel).Dec()
	connectionsPerIP.WithLabelValues(channel, clientIP).Dec()
	if reason == "" {
		reason = "unknown"
	}
	connectionsTotal.WithLabelValues(channel, "closed").Inc()
	disconnectsTotal.WithLabelValues(channel, reason).Inc()
}

func MessageInbound(channel string) {
	messagesTotal.WithLabelValues(channel, "inbound").Inc()
}

func MessageOutbound(channel string) {
	messagesTotal.WithLabelValues(channel, "outbound").Inc()
}

func ObserveMessageLatency(channel string, start time.Time) {
	messageLatency.WithLabelValues(channel).Observe(time.Since(start).Seconds())
}

func AuthFailure(channel string) {
	authFailuresTotal.WithLabelValues(channel).Inc()
}

func RateLimited(channel, scope string) {
	rateLimitedTotal.WithLabelValues(channel, scope).Inc()
}

func RecordError(channel, code string) {
	errorsTotal.WithLabelValues(channel, code).Inc()
}

func LocationRejected(channel, reason string) {
	locationRejectedTotal.WithLabelValues(channel, reason).Inc()
}

func Backpressure(channel string) {
	backpressureTotal.WithLabelValues(channel).Inc()
}

func BroadcastDropped(channel string) {
	broadcastDroppedTotal.WithLabelValues(channel).Inc()
}

func CircuitOpen(dependency string) {
	circuitOpenTotal.WithLabelValues(dependency).Inc()
}
