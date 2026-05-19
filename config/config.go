package config

import (
	"os"
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
	redisDB, _ := strconv.Atoi(getEnv("REDIS_DB", "0"))
	avgSpeed, _ := strconv.ParseFloat(getEnv("AVG_SPEED_KMH", "40"), 64)
	rateLimitMs, _ := strconv.ParseInt(getEnv("GPS_RATE_LIMIT_MS", "3000"), 10, 64)
	deviationThresh, _ := strconv.ParseFloat(getEnv("DEVIATION_THRESH_KM", "0.05"), 64)
	batchSize, _ := strconv.Atoi(getEnv("BATCH_SIZE", "500"))
	batchFlushSec, _ := strconv.ParseInt(getEnv("BATCH_FLUSH_SEC", "2"), 10, 64)
	roadGraphLoadSec, _ := strconv.ParseInt(getEnv("ROAD_GRAPH_LOAD_TIMEOUT_SEC", "180"), 10, 64)
	apiKeyEnabled, _ := strconv.ParseBool(getEnv("API_KEY_ENABLED", "false"))
	shipmentRadiusKm, _ := strconv.ParseFloat(getEnv("SHIPMENT_SEARCH_RADIUS_KM", "10"), 64)
	shipmentLimit, _ := strconv.Atoi(getEnv("SHIPMENT_SEARCH_LIMIT", "100"))
	driverRadiusKm, _ := strconv.ParseFloat(getEnv("DRIVER_SEARCH_RADIUS_KM", "20"), 64)

	corsOrigins := splitCSV(getEnv("CORS_ALLOWED_ORIGINS", "*"))
	if len(corsOrigins) == 0 {
		corsOrigins = []string{"*"}
	}

	return &Config{
		Port:               getEnv("PORT", "8080"),
		RedisAddr:          getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:      getEnv("REDIS_PASSWORD", ""),
		RedisDB:            redisDB,
		AvgSpeedKmH:        avgSpeed,
		GPSRateLimitMs:     rateLimitMs,
		PostgresDSN:        getEnv("POSTGRES_DSN", ""),
		RoadGraphRegions:   splitCSV(getEnv("ROAD_GRAPH_REGIONS", "")),
		RoadGraphLoadSec:   roadGraphLoadSec,
		DeviationThreshKm:  deviationThresh,
		BatchSize:          batchSize,
		BatchFlushSec:      batchFlushSec,
		CORSAllowedOrigins: corsOrigins,
		APIKeyEnabled:      apiKeyEnabled,
		APIKey:             getEnv("API_KEY", ""),

		ShipmentDBDriver:        getEnv("SHIPMENT_DB_DRIVER", "mysql"),
		ShipmentDBDSN:           getEnv("SHIPMENT_DB_DSN", ""),
		ShipmentTable:           getEnv("SHIPMENT_TABLE", "shipment"),
		ShipmentOriginLatColumn: getEnv("SHIPMENT_ORIGIN_LAT_COLUMN", "origin_lat"),
		ShipmentOriginLngColumn: getEnv("SHIPMENT_ORIGIN_LNG_COLUMN", "origin_lng"),
		ShipmentSearchRadiusKm:  shipmentRadiusKm,
		ShipmentSearchLimit:     shipmentLimit,

		DriverGeoKey:            getEnv("DRIVER_GEO_KEY", "drivers:geo"),
		DriverLocationStreamKey: getEnv("DRIVER_LOCATION_STREAM_KEY", "driver:locations:stream"),
		DriverSearchRadiusKm:    driverRadiusKm,
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
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
