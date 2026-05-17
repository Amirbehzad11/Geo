package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"geo-service/internal/cache"
)

const channelPrefix = "geo:events:trip:"

// Bus publishes and distributes typed events over Redis Pub/Sub.
// Any geo-service instance can publish; all instances with subscribers receive.
type Bus struct {
	redis *cache.Redis
}

func NewBus(r *cache.Redis) *Bus {
	return &Bus{redis: r}
}

// Publish serialises event and writes it to the trip's Redis channel.
func (b *Bus) Publish(ctx context.Context, event *Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("events: marshal: %w", err)
	}
	return b.redis.Publish(ctx, channelForTrip(event.TripID), data)
}

// SubscribeTrip subscribes to all events for a single trip.
// Returns a receive-only channel and a closer that must be called to free resources.
// The channel is closed when ctx is cancelled or closer is called.
func (b *Bus) SubscribeTrip(ctx context.Context, tripID int64) (<-chan *Event, func()) {
	// Use background context for the pubsub connection itself;
	// lifecycle is managed through the closer / ctx check below.
	pubsub := b.redis.Subscribe(context.Background(), channelForTrip(tripID))
	out := make(chan *Event, 512)

	go func() {
		defer close(out)
		defer pubsub.Close()
		ch := pubsub.Channel()
		for {
			select {
			case msg, ok := <-ch:
				if !ok {
					return
				}
				var ev Event
				if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
					log.Printf("[events] decode error on trip %d: %v", tripID, err)
					continue
				}
				select {
				case out <- &ev:
				default:
					// slow consumer — drop rather than block the bus goroutine
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	return out, func() { pubsub.Close() }
}

// SubscribeAll subscribes to events for ALL trips via pattern matching.
// Used by the batch writer to persist location history across every trip.
func (b *Bus) SubscribeAll(ctx context.Context) (<-chan *Event, func()) {
	pattern := channelPrefix + "*"
	pubsub := b.redis.PSubscribe(context.Background(), pattern)
	out := make(chan *Event, 2048)

	go func() {
		defer close(out)
		defer pubsub.Close()
		ch := pubsub.Channel()
		for {
			select {
			case msg, ok := <-ch:
				if !ok {
					return
				}
				var ev Event
				if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
					continue
				}
				select {
				case out <- &ev:
				default:
					// drop on slow consumer
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	return out, func() { pubsub.Close() }
}

func channelForTrip(tripID int64) string {
	return fmt.Sprintf("%s%d", channelPrefix, tripID)
}
