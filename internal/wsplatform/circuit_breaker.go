package wsplatform

import (
	"errors"
	"time"

	"github.com/sony/gobreaker"
)

var ErrCircuitOpen = errors.New("downstream circuit breaker open")

// Breaker wraps gobreaker for downstream dependency protection.
type Breaker struct {
	cb *gobreaker.CircuitBreaker
}

func NewBreaker(name string, maxFailures uint32, cooldown time.Duration) *Breaker {
	if maxFailures == 0 {
		maxFailures = 5
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	settings := gobreaker.Settings{
		Name:        name,
		MaxRequests: 2,
		Interval:    cooldown,
		Timeout:     cooldown,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= maxFailures
		},
		OnStateChange: func(_ string, from, to gobreaker.State) {
			if to == gobreaker.StateOpen {
				CircuitOpen(name)
			}
			_ = from
		},
	}
	return &Breaker{cb: gobreaker.NewCircuitBreaker(settings)}
}

func (b *Breaker) Execute(fn func() (any, error)) error {
	if b == nil || b.cb == nil {
		_, err := fn()
		return err
	}
	_, err := b.cb.Execute(fn)
	if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
		return ErrCircuitOpen
	}
	return err
}
