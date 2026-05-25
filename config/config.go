package config

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

type Config struct {
	Port               string
	RedisAddr          string
	RedisPassword      string
	RedisDB            int
	AvgSpeedKmH        float64
	GPSRateLimitMs     int64
	PostgresDSN        string // required; roads and history are stored in PostGIS
	RoadGraphRegions   []string
	RoadGraphLoadSec   int64
	DeviationThreshKm  float64  // distance from route before flagging deviation
	BatchSize          int      // location batch insert size
	BatchFlushSec      int64    // flush interval in seconds
	CORSAllowedOrigins []string // CORS_ALLOWED_ORIGINS, comma-separated; default ["*"]
	APIKeyEnabled      bool     // API_KEY_ENABLED; when true X-API-Key header is required
	APIKey             string   // API_KEY; the expected key value

	// ---- routing backend ----
	RoutingBackend         string // ROUTING_BACKEND: "internal" (default) | "osrm"
	OSRMBaseURL            string // OSRM_BASE_URL: e.g. "http://osrm:5000"
	RoutingTimeoutMs       int64  // ROUTING_TIMEOUT_MS: per-call backend deadline (default 30 000)
	RoutingMaxInFlight     int    // ROUTING_MAX_IN_FLIGHT: total concurrent route request cap (default 100)
	InternalMaxInFlight    int    // INTERNAL_ROUTING_MAX_IN_FLIGHT: internal graph search cap (default min(CPU, 4))
	OSRMMaxInFlight        int    // OSRM_ROUTING_MAX_IN_FLIGHT: concurrent OSRM call cap (default 100)
	RoutingQueueTimeoutMs  int64  // ROUTING_QUEUE_TIMEOUT_MS: semaphore wait time (default 1 000)
	RoutingYenSpurCap      int    // ROUTING_YEN_SPUR_CAP: max spur nodes per Yen's iteration (default 60; 0 = unlimited)
	RoutingMaxAlternatives int    // ROUTING_MAX_ALTERNATIVES: server-side alternatives cap (default 1)
	RouteCachePrecision    int    // ROUTE_CACHE_PRECISION: coordinate decimals in route cache keys (default 5; 6 = old behavior)

	InternalGraphEnabled  bool // INTERNAL_GRAPH_ENABLED: allow internal road graph loading/searches
	InternalGraphLazyLoad bool // INTERNAL_GRAPH_LAZY_LOAD: load internal road graph on first direct internal use
	InternalGraphRequired bool // INTERNAL_GRAPH_REQUIRED: fail startup when a required startup graph load fails

	ShipmentDBDriver        string  // SHIPMENT_DB_DRIVER; mysql or postgres/pgx
	ShipmentDBDSN           string  // SHIPMENT_DB_DSN; direct read-only connection to Laravel DB
	ShipmentTable           string  // SHIPMENT_TABLE; default shipment
	ShipmentOriginLatColumn string  // SHIPMENT_ORIGIN_LAT_COLUMN; default origin_lat
	ShipmentOriginLngColumn string  // SHIPMENT_ORIGIN_LNG_COLUMN; default origin_lng
	ShipmentSearchRadiusKm  float64 // SHIPMENT_SEARCH_RADIUS_KM; default 10
	ShipmentSearchLimit     int     // SHIPMENT_SEARCH_LIMIT; default 100

	DriverGeoKey            string  // DRIVER_GEO_KEY; Redis GEO key for live driver locations
	DriverLocationStreamKey string  // DRIVER_LOCATION_STREAM_KEY; Redis stream for async persistence
	DriverSearchRadiusKm    float64 // DRIVER_SEARCH_RADIUS_KM; default 20
}

func Load() *Config {
	corsOrigins := splitCSV(getEnv("CORS_ALLOWED_ORIGINS", "*"))
	if len(corsOrigins) == 0 {
		corsOrigins = []string{"*"}
	}

	return &Config{
		Port:               getEnv("PORT", "8080"),
		RedisAddr:          getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:      getEnv("REDIS_PASSWORD", ""),
		RedisDB:            getEnvInt("REDIS_DB", 0),
		AvgSpeedKmH:        getEnvFloat("AVG_SPEED_KMH", 40),
		GPSRateLimitMs:     getEnvInt64("GPS_RATE_LIMIT_MS", 3000),
		PostgresDSN:        getEnv("POSTGRES_DSN", ""),
		RoadGraphRegions:   splitCSV(getEnv("ROAD_GRAPH_REGIONS", "")),
		RoadGraphLoadSec:   getEnvInt64("ROAD_GRAPH_LOAD_TIMEOUT_SEC", 180),
		DeviationThreshKm:  getEnvFloat("DEVIATION_THRESH_KM", 0.05),
		BatchSize:          getEnvInt("BATCH_SIZE", 500),
		BatchFlushSec:      getEnvInt64("BATCH_FLUSH_SEC", 2),
		CORSAllowedOrigins: corsOrigins,
		APIKeyEnabled:      getEnvBool("API_KEY_ENABLED", false),
		APIKey:             getEnv("API_KEY", ""),

		ShipmentDBDriver:        getEnv("SHIPMENT_DB_DRIVER", "mysql"),
		ShipmentDBDSN:           getEnv("SHIPMENT_DB_DSN", ""),
		ShipmentTable:           getEnv("SHIPMENT_TABLE", "shipment"),
		ShipmentOriginLatColumn: getEnv("SHIPMENT_ORIGIN_LAT_COLUMN", "origin_lat"),
		ShipmentOriginLngColumn: getEnv("SHIPMENT_ORIGIN_LNG_COLUMN", "origin_lng"),
		ShipmentSearchRadiusKm:  getEnvFloat("SHIPMENT_SEARCH_RADIUS_KM", 10),
		ShipmentSearchLimit:     getEnvInt("SHIPMENT_SEARCH_LIMIT", 100),

		DriverGeoKey:            getEnv("DRIVER_GEO_KEY", "drivers:geo"),
		DriverLocationStreamKey: getEnv("DRIVER_LOCATION_STREAM_KEY", "driver:locations:stream"),
		DriverSearchRadiusKm:    getEnvFloat("DRIVER_SEARCH_RADIUS_KM", 20),

		RoutingBackend:         getEnv("ROUTING_BACKEND", "internal"),
		OSRMBaseURL:            getEnv("OSRM_BASE_URL", "http://osrm:5000"),
		RoutingTimeoutMs:       getEnvInt64("ROUTING_TIMEOUT_MS", 30000),
		RoutingMaxInFlight:     getEnvInt("ROUTING_MAX_IN_FLIGHT", 100),
		InternalMaxInFlight:    getEnvInt("INTERNAL_ROUTING_MAX_IN_FLIGHT", defaultInternalRoutingLimit()),
		OSRMMaxInFlight:        getEnvInt("OSRM_ROUTING_MAX_IN_FLIGHT", 100),
		RoutingQueueTimeoutMs:  getEnvInt64("ROUTING_QUEUE_TIMEOUT_MS", 1000),
		RoutingYenSpurCap:      getEnvInt("ROUTING_YEN_SPUR_CAP", 60),
		RoutingMaxAlternatives: getEnvInt("ROUTING_MAX_ALTERNATIVES", 1),
		RouteCachePrecision:    getEnvInt("ROUTE_CACHE_PRECISION", 5),
		InternalGraphEnabled:   getEnvBool("INTERNAL_GRAPH_ENABLED", true),
		InternalGraphLazyLoad:  getEnvBool("INTERNAL_GRAPH_LAZY_LOAD", false),
		InternalGraphRequired:  getEnvBool("INTERNAL_GRAPH_REQUIRED", true),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func getEnvInt64(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return fallback
}

func getEnvFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

func splitCSV(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func defaultInternalRoutingLimit() int {
	n := runtime.NumCPU()
	if n <= 0 {
		return 1
	}
	if n > 4 {
		return 4
	}
	return n
}
