package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Port              string
	RedisAddr         string
	RedisPassword     string
	RedisDB           int
	AvgSpeedKmH       float64
	GPSRateLimitMs    int64
	PostgresDSN       string   // required; roads and history are stored in PostGIS
	RoadGraphRegions  []string
	RoadGraphLoadSec  int64
	DeviationThreshKm float64  // distance from route before flagging deviation
	BatchSize         int      // location batch insert size
	BatchFlushSec     int64    // flush interval in seconds
	CORSAllowedOrigins []string // CORS_ALLOWED_ORIGINS, comma-separated; default ["*"]
	APIKeyEnabled      bool     // API_KEY_ENABLED; when true X-API-Key header is required
	APIKey             string   // API_KEY; the expected key value
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
