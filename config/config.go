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
	JWTAuthEnabled     bool     // JWT_AUTH_ENABLED; when true Authorization: Bearer JWT is accepted
	JWTSecret          string   // JWT_SECRET; must match Laravel tymon/jwt-auth secret for HS* algorithms
	JWTAlgorithm       string   // JWT_ALGO; HS256, HS384, or HS512
	RateLimitPerMin    int      // RATE_LIMIT_PER_MINUTE; per-IP HTTP request cap
	RequestBodyLimit   int64    // REQUEST_BODY_LIMIT_BYTES; max accepted request body size

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

	ShipmentDBDriver             string  // SHIPMENT_DB_DRIVER; mysql or postgres/pgx
	ShipmentDBDSN                string  // SHIPMENT_DB_DSN; direct read-only connection to Laravel DB
	ShipmentTable                string  // SHIPMENT_TABLE; default shipments
	ShipmentOriginLocationColumn string  // SHIPMENT_ORIGIN_LOCATION_COLUMN; PostGIS geometry column for origin (e.g. start_location)
	ShipmentEndLocationColumn    string  // SHIPMENT_END_LOCATION_COLUMN; PostGIS geometry column for destination (e.g. end_location)
	ShipmentSearchRadiusKm       float64 // SHIPMENT_SEARCH_RADIUS_KM; default 2
	ShipmentSearchLimit          int     // SHIPMENT_SEARCH_LIMIT; default 100
	ShipmentWeightColumn         string  // SHIPMENT_WEIGHT_COLUMN; legacy weight column retained for compatibility

	// Vehicle lookup settings.
	// VehicleTypesTable / VehicleTypeLabelColumn / VehicleTypeTitleColumn drive the vehicles array.
	// The vehicle_box_sizes settings remain for legacy compatibility only.
	VehicleBoxSizesTable   string // VEHICLE_BOX_SIZES_TABLE; default "vehicle_box_sizes"
	VehicleIDColumn        string // VEHICLE_ID_COLUMN; FK column in vehicle_box_sizes; default "vehicle_id"
	VehicleWeightColumn    string // VEHICLE_WEIGHT_COLUMN; max_weight column; default "max_weight"
	VehicleTypesTable      string // VEHICLE_TYPES_TABLE; joined for label; default "vehicle_types"
	VehicleTypeLabelColumn string // VEHICLE_TYPE_LABEL_COLUMN; label column in vehicle_types; default "label"
	VehicleTypeTitleColumn string // VEHICLE_TYPE_TITLE_COLUMN; title column in vehicle_types; default "title"

	// Content types (optional) — joins content_types on content_type_id to add
	// content_image to each shipment result. Set CONTENT_TYPES_TABLE to enable.
	ContentTypesTable      string // CONTENT_TYPES_TABLE; e.g. "content_types"; default "" (disabled)
	ContentTypeIDColumn    string // CONTENT_TYPE_ID_COLUMN; FK in shipments; default "content_type_id"
	ContentTypeImageColumn string // CONTENT_TYPE_IMAGE_COLUMN; image column in content_types; default "image"

	// Shipment images (optional) — adds an images array to nearby shipment rows.
	ShipmentImagesTable           string // SHIPMENT_IMAGES_TABLE; e.g. "shipment_images"; default "" (disabled)
	ShipmentImageShipmentIDColumn string // SHIPMENT_IMAGE_SHIPMENT_ID_COLUMN; default "shipment_id"
	ShipmentImageColumn           string // SHIPMENT_IMAGE_COLUMN; default "image"

	DriverGeoKey            string  // DRIVER_GEO_KEY; Redis GEO key for live driver locations
	DriverLocationStreamKey string  // DRIVER_LOCATION_STREAM_KEY; Redis stream for async persistence
	DriverSearchRadiusKm    float64 // DRIVER_SEARCH_RADIUS_KM; default 20

	// WebSocketTripAuthEnabled gates JWT/API-key + trip ACL on GET /ws/trip/:id.
	WebSocketTripAuthEnabled bool // WEBSOCKET_AUTH_ENABLED; default false (dev-friendly)

	// Shipment nearby WebSocket security (/ws/shipments/nearby).
	WSShipmentRequireTLS      bool
	WSShipmentAuthRequired    bool
	WSShipmentLegacyFormat    bool
	WSShipmentMaxPayloadBytes int64
	WSShipmentMaxConnections  int
	WSShipmentMaxPerIP        int
	WSShipmentMessagesPerSec  float64
	WSShipmentMessageBurst    int
	WSShipmentIdleTimeoutSec  int64
	WSShipmentPingIntervalSec int64
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
		JWTAuthEnabled:     getEnvBool("JWT_AUTH_ENABLED", true),
		JWTSecret:          getEnv("JWT_SECRET", ""),
		JWTAlgorithm:       getEnv("JWT_ALGO", "HS256"),
		RateLimitPerMin:    getEnvInt("RATE_LIMIT_PER_MINUTE", 300),
		RequestBodyLimit:   getEnvInt64("REQUEST_BODY_LIMIT_BYTES", 1<<20),

		ShipmentDBDriver:             getEnv("SHIPMENT_DB_DRIVER", "postgres"),
		ShipmentDBDSN:                getEnv("SHIPMENT_DB_DSN", ""),
		ShipmentTable:                getEnv("SHIPMENT_TABLE", "shipments"),
		ShipmentOriginLocationColumn: getEnv("SHIPMENT_ORIGIN_LOCATION_COLUMN", "start_location"),
		ShipmentEndLocationColumn:    getEnv("SHIPMENT_END_LOCATION_COLUMN", "end_location"),
		ShipmentSearchRadiusKm:       getEnvFloat("SHIPMENT_SEARCH_RADIUS_KM", 2),
		ShipmentSearchLimit:          getEnvInt("SHIPMENT_SEARCH_LIMIT", 100),
		ShipmentWeightColumn:         getEnv("SHIPMENT_WEIGHT_COLUMN", "package_length"),

		VehicleBoxSizesTable:   getEnv("VEHICLE_BOX_SIZES_TABLE", "vehicle_box_sizes"),
		VehicleIDColumn:        getEnv("VEHICLE_ID_COLUMN", "vehicle_id"),
		VehicleWeightColumn:    getEnv("VEHICLE_WEIGHT_COLUMN", "standard_length"),
		VehicleTypesTable:      getEnv("VEHICLE_TYPES_TABLE", "vehicle_types"),
		VehicleTypeLabelColumn: getEnv("VEHICLE_TYPE_LABEL_COLUMN", "label"),
		VehicleTypeTitleColumn: getEnv("VEHICLE_TYPE_TITLE_COLUMN", "title"),

		ContentTypesTable:      getEnv("CONTENT_TYPES_TABLE", ""),
		ContentTypeIDColumn:    getEnv("CONTENT_TYPE_ID_COLUMN", "content_type_id"),
		ContentTypeImageColumn: getEnv("CONTENT_TYPE_IMAGE_COLUMN", "image"),

		ShipmentImagesTable:           getEnv("SHIPMENT_IMAGES_TABLE", ""),
		ShipmentImageShipmentIDColumn: getEnv("SHIPMENT_IMAGE_SHIPMENT_ID_COLUMN", "shipment_id"),
		ShipmentImageColumn:           getEnv("SHIPMENT_IMAGE_COLUMN", "image"),

		DriverGeoKey:            getEnv("DRIVER_GEO_KEY", "drivers:geo"),
		DriverLocationStreamKey: getEnv("DRIVER_LOCATION_STREAM_KEY", "driver:locations:stream"),
		DriverSearchRadiusKm:    getEnvFloat("DRIVER_SEARCH_RADIUS_KM", 20),

		WebSocketTripAuthEnabled: getEnvBool("WEBSOCKET_AUTH_ENABLED", false),

		WSShipmentRequireTLS:      getEnvBool("WS_SHIPMENT_REQUIRE_TLS", false),
		WSShipmentAuthRequired:    getEnvBool("WS_SHIPMENT_AUTH_REQUIRED", true),
		WSShipmentLegacyFormat:    getEnvBool("WS_SHIPMENT_LEGACY_FORMAT", true),
		WSShipmentMaxPayloadBytes: getEnvInt64("WS_SHIPMENT_MAX_PAYLOAD_BYTES", 4096),
		WSShipmentMaxConnections:  getEnvInt("WS_SHIPMENT_MAX_CONNECTIONS", 2000),
		WSShipmentMaxPerIP:        getEnvInt("WS_SHIPMENT_MAX_PER_IP", 10),
		WSShipmentMessagesPerSec:  getEnvFloat("WS_SHIPMENT_MESSAGES_PER_SEC", 2),
		WSShipmentMessageBurst:    getEnvInt("WS_SHIPMENT_MESSAGE_BURST", 5),
		WSShipmentIdleTimeoutSec:  getEnvInt64("WS_SHIPMENT_IDLE_TIMEOUT_SEC", 90),
		WSShipmentPingIntervalSec: getEnvInt64("WS_SHIPMENT_PING_INTERVAL_SEC", 30),

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
