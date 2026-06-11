package route

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"geo-service/internal/cache"
	"geo-service/internal/routing"
)

// ---- stub backend -----------------------------------------------------------

type stubBackend struct {
	name string
	fn   func(ctx context.Context) (*RouteResponse, error)
}

func (s *stubBackend) BackendName() string { return s.name }

func (s *stubBackend) ComputeRoute(
	ctx context.Context, _ routing.TransportMode, _ int, _, _, _, _ float64,
) (*RouteResponse, error) {
	return s.fn(ctx)
}

func okBackend(name string) *stubBackend {
	return &stubBackend{name: name, fn: func(_ context.Context) (*RouteResponse, error) {
		return &RouteResponse{Distance: 10, Duration: 5, Mode: "car"}, nil
	}}
}

func errBackend(name string) *stubBackend {
	return &stubBackend{name: name, fn: func(_ context.Context) (*RouteResponse, error) {
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
	secondary := &stubBackend{name: "internal", fn: func(_ context.Context) (*RouteResponse, error) {
		return &RouteResponse{Distance: 99, Duration: 20, Mode: "car"}, nil
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

func TestFallbackBackend_DoesNotLazyLoadInternalGraphOnPrimaryFailure(t *testing.T) {
	loads := 0
	internal := NewInternalBackendWithConfig(InternalBackendConfig{
		AvgSpeedKmH:  40,
		GraphEnabled: true,
		LazyLoad:     true,
		LoadEngine: func(context.Context) (*routing.Engine, error) {
			loads++
			return nil, errors.New("should not load")
		},
	})
	fb := NewFallbackBackend(errBackend("osrm"), internal)

	_, err := fb.ComputeRoute(context.Background(), routing.ModeCar, 1, 35, 51, 36, 52)
	if !errors.Is(err, ErrRoutingBackendUnavailable) {
		t.Fatalf("expected backend unavailable, got %v", err)
	}
	if loads != 0 {
		t.Fatalf("fallback should not synchronously lazy-load internal graph, loads=%d", loads)
	}
}

func TestInternalBackend_DisabledGraphReturnsUnavailableForGroundModes(t *testing.T) {
	internal := NewInternalBackendWithConfig(InternalBackendConfig{
		AvgSpeedKmH:  40,
		GraphEnabled: false,
	})

	_, err := internal.ComputeRoute(context.Background(), routing.ModeCar, 1, 35, 51, 36, 52)
	if !errors.Is(err, ErrRoutingBackendUnavailable) {
		t.Fatalf("expected backend unavailable, got %v", err)
	}
}

func TestInternalBackend_DisabledGraphStillServesAirplane(t *testing.T) {
	internal := NewInternalBackendWithConfig(InternalBackendConfig{
		AvgSpeedKmH:  40,
		GraphEnabled: false,
	})

	resp, err := internal.ComputeRoute(context.Background(), routing.ModeAirplane, 1, 35, 51, 36, 52)
	if err != nil {
		t.Fatalf("airplane route should not need road graph: %v", err)
	}
	if resp.Distance <= 0 || len(resp.Primary.Polyline) == 0 {
		t.Fatalf("expected airplane response with distance and polyline, got %+v", resp)
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

func TestLimitedBackend_ReturnsOverloadedWhenSlotsFull(t *testing.T) {
	limited := NewLimitedBackend(okBackend("internal"), 1, 10*time.Millisecond, "internal")
	limited.sem <- struct{}{}
	defer func() { <-limited.sem }()

	_, err := limited.ComputeRoute(context.Background(), routing.ModeCar, 1, 0, 0, 1, 1)
	if !errors.Is(err, ErrRoutingOverloaded) {
		t.Fatalf("expected overloaded error, got %v", err)
	}
}

func TestTypedRouteErrors(t *testing.T) {
	if err := classifyOSRMError(context.DeadlineExceeded); !errors.Is(err, ErrRoutingTimeout) {
		t.Fatalf("deadline should classify as timeout, got %v", err)
	}
	if err := classifyOSRMError(errors.New("osrm: routing code \"NoRoute\"")); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("NoRoute should classify as not found, got %v", err)
	}
	if err := classifyOSRMError(errors.New("connection refused")); !errors.Is(err, ErrRoutingBackendUnavailable) {
		t.Fatalf("connection failure should classify as unavailable, got %v", err)
	}
}

func TestNormalizeAlternativesUsesServerSideMax(t *testing.T) {
	if got := normalizeAlternatives(5, 1); got != 1 {
		t.Fatalf("normalizeAlternatives(5, 1) = %d, want 1", got)
	}
	if got := normalizeAlternatives(0, 2); got != 1 {
		t.Fatalf("normalizeAlternatives(0, 2) = %d, want default 1", got)
	}
	if got := normalizeAlternatives(5, 99); got != maxRouteAlternatives {
		t.Fatalf("normalizeAlternatives should never exceed hard max %d, got %d", maxRouteAlternatives, got)
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
				return &RouteResponse{Distance: 5, Mode: "car"}, nil
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

func TestCacheKeyPrecision_RoundsNearbyCoordinates(t *testing.T) {
	keyA := cache.RouteKeyWithPrecision("osrm", "car", 1, 5, 35.689201, 51.389001, 35.700001, 51.400001)
	keyB := cache.RouteKeyWithPrecision("osrm", "car", 1, 5, 35.689202, 51.389002, 35.700002, 51.400002)
	if keyA != keyB {
		t.Fatalf("precision=5 should reuse nearby route keys:\n%s\n%s", keyA, keyB)
	}

	legacyA := cache.RouteKey("osrm", "car", 1, 35.689201, 51.389001, 35.700001, 51.400001)
	legacyB := cache.RouteKey("osrm", "car", 1, 35.689202, 51.389002, 35.700002, 51.400002)
	if legacyA == legacyB {
		t.Fatalf("legacy precision=6 should keep these keys distinct")
	}
}
