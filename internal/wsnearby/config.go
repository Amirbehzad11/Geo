package wsnearby

import "time"

// Config holds production security settings for /ws/shipments/nearby.
type Config struct {
	RequireTLS      bool
	RequireAuth     bool
	AllowLegacy     bool
	MaxPayloadBytes int64
	MaxGlobalConns  int
	MaxPerIP        int
	MessagesPerSec  float64
	MessageBurst    int
	IdleTimeout     time.Duration
	PingInterval    time.Duration
	QueryTimeout    time.Duration
}

// DefaultConfig returns sensible production defaults.
func DefaultConfig() Config {
	return Config{
		RequireTLS:      false,
		RequireAuth:     true,
		AllowLegacy:     true,
		MaxPayloadBytes: 4096,
		MaxGlobalConns:  2000,
		MaxPerIP:        10,
		MessagesPerSec:  2,
		MessageBurst:    5,
		IdleTimeout:     90 * time.Second,
		PingInterval:    30 * time.Second,
		QueryTimeout:    10 * time.Second,
	}
}
