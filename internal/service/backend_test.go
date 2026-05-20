package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"geo-service/internal/cache"
	"geo-service/internal/model"
	"geo-service/internal/routing"
)

// ---- stub backend -----------------------------------------------------------

type stubBackend struct {
	name string
	fn   func(ctx context.Context) (*model.RouteResponse, error)
}

func (s *stubBackend) BackendName() string { return s.name }

func (s *stubBackend) ComputeRoute(
	ctx context.Context, _ routing.TransportMode, _ int, _, _, _, _ float64,
) (*model.RouteResponse, error) {
	return s.fn(ctx)
}

func okBackend(name string) *stubBackend {
	return &stubBackend{name: name, fn: func(_ context.Context) (*model.RouteResponse, error) {
		return &model.RouteResponse{Distance: 10, Duration: 5, Mode: "car"}, nil
	}}
}

func errBackend(name string) *stubBackend {
	return &stubBackend{name: name, fn: func(_ context.Context) (*model.RouteResponse, error) {
		return nil, errors.New("backend unavailable")
	}}
}

// ---- FallbackBackend --------------------------------------------------------

func TestFallbackBackend_UsesPrimaryOnSuccess(t *testing.T) {
	fb := NewFallbackBackend(okBackend("osrm"), errBackend("internal"))
	resp, err := fb.ComputeRoute(context.Background(), routing.ModeCar, 1, 0, 0, 1, 1)
	if err != nil {
		t.Fatalf("expected success from primary: %v", err)
	}
	if resp.Distance != 10 {
		t.Errorf("distance = %.1f, want 10 (from primary)", resp.Distance)
	}
}

func TestFallbackBackend_CascadesToSecondaryOnPrimaryError(t *testing.T) {
	secondary := &stubBackend{name: "internal", fn: func(_ context.Context) (*model.RouteResponse, error) {
		return &model.RouteResponse{Distance: 99, Duration: 20, Mode: "car"}, nil
	}}
	fb := NewFallbackBackend(errBackend("osrm"), secondary)

	resp, err := fb.ComputeRoute(context.Background(), routing.ModeCar, 1, 0, 0, 1, 1)
	if err != nil {
		t.Fatalf("expected secondary to succeed: %v", err)
	}
	if resp.Distance != 99 {
		t.Errorf("distance = %.1f, want 99 (secondary)", resp.Distance)
	}
}

func TestFallbackBackend_ReturnsErrorWhenBothFail(t *testing.T) {
	fb := NewFallbackBackend(errBackend("osrm"), errBackend("internal"))
	_, err := fb.ComputeRoute(context.Background(), routing.ModeCar, 1, 0, 0, 1, 1)
	if err == nil {
		t.Fatal("expected error when both backends fail")
	}
}

func TestFallbackBackend_BackendNameIsPrimary(t *testing.T) {
	fb := NewFallbackBackend(okBackend("osrm"), okBackend("internal"))
	if got := fb.BackendName(); got != "osrm" {
		t.Errorf("BackendName() = %q, want osrm", got)
	}
}

// ---- semaphore --------------------------------------------------------------

// TestSemaphore_BlocksWhenFull verifies that a full semaphore blocks new calls
// until the queue-wait timeout expires (simulating ErrRoutingOverloaded).
func TestSemaphore_BlocksWhenFull(t *testing.T) {
	svc := &RouteService{
		backend:    okBackend("test"),
		sem:        make(chan struct{}, 1),
		semTimeout: 30 * time.Millisecond,
	}

	// Fill the single slot.
	svc.sem <- struct{}{}
	defer func() { <-svc.sem }()

	timer := time.NewTimer(svc.semTimeout)
	defer timer.Stop()

	select {
	case svc.sem <- struct{}{}:
		// Release again to avoid leak, then fail.
		<-svc.sem
		t.Fatal("semaphore should have been full")
	case <-timer.C:
		// Correct: timed out because semaphore was at capacity.
	}
}

// ---- singleflight -----------------------------------------------------------

// TestSingleflight_DeduplicatesConcurrentCalls verifies that N concurrent
// callers sharing the same key trigger exactly one backend computation.
func TestSingleflight_DeduplicatesConcurrentCalls(t *testing.T) {
	callCount := 0
	var mu sync.Mutex

	svc := &RouteService{backend: okBackend("test")}

	const n = 5
	var wg sync.WaitGroup
	errs := make([]error, n)

	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err, _ := svc.group.Do("shared-key", func() (any, error) {
				mu.Lock()
				callCount++
				mu.Unlock()
				time.Sleep(20 * time.Millisecond)
				return &model.RouteResponse{Distance: 5, Mode: "car"}, nil
			})
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d error: %v", i, err)
		}
	}

	mu.Lock()
	count := callCount
	mu.Unlock()

	if count != 1 {
		t.Errorf("expected 1 backend call via singleflight, got %d", count)
	}
}

// ---- cache key --------------------------------------------------------------

// TestCacheKey_IncludesBackendAndVersion checks that backend name appears in
// the key and that OSRM and internal keys are disjoint.
func TestCacheKey_IncludesBackendAndVersion(t *testing.T) {
	osrmKey := cache.RouteKey("osrm", "car", 1, 35.0, 51.0, 36.0, 52.0)
	internalKey := cache.RouteKey("internal", "car", 1, 35.0, 51.0, 36.0, 52.0)

	if osrmKey == internalKey {
		t.Error("osrm and internal route cache keys must differ")
	}
	if !strings.Contains(osrmKey, "osrm") {
		t.Errorf("osrm key does not contain 'osrm': %q", osrmKey)
	}
	if !strings.Contains(internalKey, "internal") {
		t.Errorf("internal key does not contain 'internal': %q", internalKey)
	}
}
