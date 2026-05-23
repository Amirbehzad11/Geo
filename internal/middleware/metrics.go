package middleware

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// RouteCalcTotal counts completed route calculations.
	RouteCalcTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "geo_route_calculations_total",
		Help: "Total number of route calculations.",
	})

	// RouteCalcLatency tracks route calculation duration in seconds.
	RouteCalcLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "geo_route_calculation_duration_seconds",
		Help:    "Route calculation latency distribution.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
	})

	RouteCacheTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "geo_route_cache_total",
		Help: "Route cache lookups by result.",
	}, []string{"result"})

	RouteBackendTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "geo_route_backend_total",
		Help: "Route backend attempts by backend and result.",
	}, []string{"backend", "result"})

	RouteInternalQueueWaitDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "geo_route_internal_queue_wait_seconds",
		Help:    "Time spent waiting for an internal routing worker slot.",
		Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
	})

	RouteInternalActiveWorkers = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "geo_route_internal_active_workers",
		Help: "Currently active internal routing workers.",
	})

	RouteTimeoutTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "geo_route_timeout_total",
		Help: "Route requests that timed out by backend.",
	}, []string{"backend"})

	RouteOverloadTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "geo_route_overload_total",
		Help: "Route requests rejected by concurrency limit scope.",
	}, []string{"scope"})

	// GPSUpdateTotal counts GPS position updates processed.
	GPSUpdateTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "geo_gps_updates_total",
		Help: "Total GPS updates processed.",
	})

	// WSConnections tracks the number of active WebSocket connections.
	WSConnections = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "geo_websocket_connections_active",
		Help: "Currently active WebSocket connections.",
	})

	httpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "geo_http_requests_total",
		Help: "Total HTTP requests by method, path, and status.",
	}, []string{"method", "path", "status"})

	httpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "geo_http_request_duration_seconds",
		Help:    "HTTP request latency.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})
)

// RequestMetrics is a Gin middleware that records per-request Prometheus metrics.
func RequestMetrics() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		path := c.FullPath()
		if path == "" {
			path = "unknown"
		}
		elapsed := time.Since(start)
		status := strconv.Itoa(c.Writer.Status())

		httpRequestsTotal.WithLabelValues(c.Request.Method, path, status).Inc()
		httpRequestDuration.WithLabelValues(c.Request.Method, path).Observe(elapsed.Seconds())
	}
}
