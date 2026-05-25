package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"geo-service/internal/middleware"
	"geo-service/internal/model"
	"geo-service/internal/osrmclient"
	"geo-service/internal/routing"
)

// RouteBackend computes a route and returns a RouteResponse.
// Implementations are free to call OSRM, the internal A* engine, or chain both.
type RouteBackend interface {
	// ComputeRoute performs the actual routing computation.
	// The ctx carries the deadline for the call; implementations must honor it.
	ComputeRoute(
		ctx context.Context,
		mode routing.TransportMode,
		alternatives int,
		startLat, startLng, endLat, endLng float64,
	) (*model.RouteResponse, error)

	// BackendName returns a short identifier included in cache keys so that
	// internal and OSRM results are stored separately.
	BackendName() string
}

// RouteComputeResult carries backend metadata without changing the public API.
type RouteComputeResult struct {
	Response *model.RouteResponse
	Backend  string
}

type routeBackendWithResult interface {
	ComputeRouteResult(
		ctx context.Context,
		mode routing.TransportMode,
		alternatives int,
		startLat, startLng, endLat, endLng float64,
	) (*RouteComputeResult, error)
}

type modeReadiness interface {
	CanServeModeWithoutLoad(mode routing.TransportMode) bool
}

func computeRouteResult(
	backend RouteBackend,
	ctx context.Context,
	mode routing.TransportMode,
	alternatives int,
	startLat, startLng, endLat, endLng float64,
) (*RouteComputeResult, error) {
	if b, ok := backend.(routeBackendWithResult); ok {
		return b.ComputeRouteResult(ctx, mode, alternatives, startLat, startLng, endLat, endLng)
	}
	resp, err := backend.ComputeRoute(ctx, mode, alternatives, startLat, startLng, endLat, endLng)
	if err != nil {
		return nil, err
	}
	return &RouteComputeResult{Response: resp, Backend: backend.BackendName()}, nil
}

// ---- InternalBackend -------------------------------------------------------

type InternalBackendConfig struct {
	Engine       *routing.Engine
	AvgSpeedKmH  float64
	GraphEnabled bool
	LazyLoad     bool
	LoadEngine   func(context.Context) (*routing.Engine, error)
}

// InternalBackend wraps the in-process routing engine.
// Airplane mode never requires the road graph.
type InternalBackend struct {
	mu           sync.Mutex
	engine       *routing.Engine
	avgSpeedKmH  float64
	graphEnabled bool
	lazyLoad     bool
	loadEngine   func(context.Context) (*routing.Engine, error)
}

// NewInternalBackend creates an InternalBackend backed by the given Engine.
func NewInternalBackend(engine *routing.Engine) *InternalBackend {
	return NewInternalBackendWithConfig(InternalBackendConfig{
		Engine:       engine,
		AvgSpeedKmH:  40,
		GraphEnabled: true,
	})
}

func NewInternalBackendWithConfig(cfg InternalBackendConfig) *InternalBackend {
	if cfg.AvgSpeedKmH <= 0 {
		cfg.AvgSpeedKmH = 40
	}
	return &InternalBackend{
		engine:       cfg.Engine,
		avgSpeedKmH:  cfg.AvgSpeedKmH,
		graphEnabled: cfg.GraphEnabled,
		lazyLoad:     cfg.LazyLoad,
		loadEngine:   cfg.LoadEngine,
	}
}

func (b *InternalBackend) BackendName() string { return "internal" }

func (b *InternalBackend) CanServeModeWithoutLoad(mode routing.TransportMode) bool {
	if mode == routing.ModeAirplane {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.engine == nil {
		return false
	}
	switch mode {
	case routing.ModeTrain:
		return b.engine.HasRailGraph()
	case routing.ModePublicTransport:
		return b.engine.HasTransitGraph()
	default:
		return b.engine.HasGraph()
	}
}

func (b *InternalBackend) ComputeRoute(
	ctx context.Context,
	mode routing.TransportMode,
	alternatives int,
	startLat, startLng, endLat, endLng float64,
) (*model.RouteResponse, error) {
	result, err := b.ComputeRouteResult(ctx, mode, alternatives, startLat, startLng, endLat, endLng)
	if err != nil {
		return nil, err
	}
	return result.Response, nil
}

func (b *InternalBackend) ComputeRouteResult(
	ctx context.Context,
	mode routing.TransportMode,
	alternatives int,
	startLat, startLng, endLat, endLng float64,
) (*RouteComputeResult, error) {
	engine, err := b.engineFor(ctx, mode)
	if err != nil {
		recordBackendAttempt("internal", err)
		return nil, err
	}

	if mode == routing.ModeTrain {
		legs := engine.ComputeTrainRoute(ctx, startLat, startLng, endLat, endLng)
		if legs == nil || len(legs) == 0 {
			err := fmt.Errorf("%w: no rail route found between the two points", ErrRouteNotFound)
			recordBackendAttempt("internal", err)
			return nil, err
		}
		resp := buildMultiModalTrainResponse(legs)
		recordBackendAttempt("internal", nil)
		return &RouteComputeResult{Response: resp, Backend: "internal"}, nil
	}

	if mode == routing.ModePublicTransport {
		legs := engine.ComputeTransitRoute(ctx, startLat, startLng, endLat, endLng)
		if legs == nil || len(legs) == 0 {
			err := fmt.Errorf("%w: no public transport route found between the two points", ErrRouteNotFound)
			recordBackendAttempt("internal", err)
			return nil, err
		}
		resp := buildMultiModalTransitResponse(legs)
		recordBackendAttempt("internal", nil)
		return &RouteComputeResult{Response: resp, Backend: "internal"}, nil
	}

	routes := engine.CalculateAlternativesCtx(ctx, startLat, startLng, endLat, endLng, mode, alternatives)
	if len(routes) == 0 {
		err := classifyInternalRouteError(ctx)
		recordBackendAttempt("internal", err)
		return nil, err
	}

	resp := buildRouteResponse(mode, routes)
	recordBackendAttempt("internal", nil)
	return &RouteComputeResult{Response: resp, Backend: "internal"}, nil
}

func (b *InternalBackend) engineFor(ctx context.Context, mode routing.TransportMode) (*routing.Engine, error) {
	b.mu.Lock()
	engine := b.engine
	b.mu.Unlock()

	if mode == routing.ModeAirplane {
		if engine != nil {
			return engine, nil
		}
		return routing.NewEngineWithGraph(b.avgSpeedKmH, nil, 0), nil
	}

	if mode == routing.ModeTrain {
		if engine != nil && engine.HasRailGraph() {
			return engine, nil
		}
		return nil, fmt.Errorf("%w: rail graph is not loaded (set RAIL_GRAPH_ENABLED=true)", ErrRoutingBackendUnavailable)
	}

	if mode == routing.ModePublicTransport {
		if engine != nil && engine.HasTransitGraph() {
			return engine, nil
		}
		return nil, fmt.Errorf("%w: transit graph is not loaded (run osm2transit to import bus/metro data)", ErrRoutingBackendUnavailable)
	}

	if engine != nil && engine.HasGraph() {
		return engine, nil
	}
	if !b.graphEnabled {
		return nil, fmt.Errorf("%w: internal road graph is disabled", ErrRoutingBackendUnavailable)
	}
	if !b.lazyLoad || b.loadEngine == nil {
		return nil, fmt.Errorf("%w: internal road graph is not loaded", ErrRoutingBackendUnavailable)
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.engine != nil && b.engine.HasGraph() {
		return b.engine, nil
	}

	slog.Info("lazy loading internal road graph")
	loaded, err := b.loadEngine(ctx)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: internal graph lazy load timed out: %v", ErrRoutingTimeout, err)
		}
		return nil, fmt.Errorf("%w: internal graph lazy load failed: %v", ErrRoutingBackendUnavailable, err)
	}
	if loaded == nil || !loaded.HasGraph() {
		return nil, fmt.Errorf("%w: internal graph lazy load returned no graph", ErrRoutingBackendUnavailable)
	}
	b.engine = loaded
	return loaded, nil
}

func classifyInternalRouteError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%w: internal route search deadline exceeded", ErrRoutingTimeout)
		}
		return fmt.Errorf("%w: internal route search cancelled: %v", ErrRoutingTimeout, err)
	}
	return fmt.Errorf("%w: internal engine returned no route", ErrRouteNotFound)
}

// ---- OSRMBackend -----------------------------------------------------------

// OSRMBackend delegates routing to a self-hosted OSRM instance.
type OSRMBackend struct {
	client *osrmclient.Client
}

// NewOSRMBackend creates an OSRMBackend that connects to baseURL with the
// given per-request timeout.
func NewOSRMBackend(baseURL string, timeout time.Duration) *OSRMBackend {
	return &OSRMBackend{client: osrmclient.New(baseURL, timeout)}
}

func (b *OSRMBackend) BackendName() string { return "osrm" }

func (b *OSRMBackend) ComputeRoute(
	ctx context.Context,
	mode routing.TransportMode,
	alternatives int,
	startLat, startLng, endLat, endLng float64,
) (*model.RouteResponse, error) {
	result, err := b.ComputeRouteResult(ctx, mode, alternatives, startLat, startLng, endLat, endLng)
	if err != nil {
		return nil, err
	}
	return result.Response, nil
}

func (b *OSRMBackend) ComputeRouteResult(
	ctx context.Context,
	mode routing.TransportMode,
	_ int,
	startLat, startLng, endLat, endLng float64,
) (*RouteComputeResult, error) {
	resp, err := b.client.Route(ctx, mode, startLat, startLng, endLat, endLng)
	if err != nil {
		err = classifyOSRMError(err)
		recordBackendAttempt("osrm", err)
		return nil, err
	}
	recordBackendAttempt("osrm", nil)
	return &RouteComputeResult{Response: resp, Backend: "osrm"}, nil
}

func classifyOSRMError(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "client.timeout") ||
		strings.Contains(msg, "timeout") {
		return fmt.Errorf("%w: %v", ErrRoutingTimeout, err)
	}
	if strings.Contains(msg, "noroute") || strings.Contains(msg, "no route") || strings.Contains(msg, "no routes") {
		return fmt.Errorf("%w: %v", ErrRouteNotFound, err)
	}
	return fmt.Errorf("%w: %v", ErrRoutingBackendUnavailable, err)
}

// ---- LimitedBackend --------------------------------------------------------

type LimitedBackend struct {
	backend      RouteBackend
	name         string
	sem          chan struct{}
	queueTimeout time.Duration
}

func NewLimitedBackend(backend RouteBackend, maxInFlight int, queueTimeout time.Duration, name string) *LimitedBackend {
	if name == "" {
		name = backend.BackendName()
	}
	var sem chan struct{}
	if maxInFlight > 0 {
		sem = make(chan struct{}, maxInFlight)
	}
	if queueTimeout <= 0 {
		queueTimeout = time.Second
	}
	return &LimitedBackend{
		backend:      backend,
		name:         name,
		sem:          sem,
		queueTimeout: queueTimeout,
	}
}

func (b *LimitedBackend) BackendName() string { return b.backend.BackendName() }

func (b *LimitedBackend) CanServeModeWithoutLoad(mode routing.TransportMode) bool {
	if ready, ok := b.backend.(modeReadiness); ok {
		return ready.CanServeModeWithoutLoad(mode)
	}
	return true
}

func (b *LimitedBackend) ComputeRoute(
	ctx context.Context,
	mode routing.TransportMode,
	alternatives int,
	startLat, startLng, endLat, endLng float64,
) (*model.RouteResponse, error) {
	result, err := b.ComputeRouteResult(ctx, mode, alternatives, startLat, startLng, endLat, endLng)
	if err != nil {
		return nil, err
	}
	return result.Response, nil
}

func (b *LimitedBackend) ComputeRouteResult(
	ctx context.Context,
	mode routing.TransportMode,
	alternatives int,
	startLat, startLng, endLat, endLng float64,
) (*RouteComputeResult, error) {
	if b.sem == nil || (b.name == "internal" && mode == routing.ModeAirplane) {
		return computeRouteResult(b.backend, ctx, mode, alternatives, startLat, startLng, endLat, endLng)
	}

	waitStart := time.Now()
	timer := time.NewTimer(b.queueTimeout)
	defer timer.Stop()
	select {
	case b.sem <- struct{}{}:
	case <-timer.C:
		err := fmt.Errorf("%w: %s worker slots are full", ErrRoutingOverloaded, b.name)
		middleware.RouteOverloadTotal.WithLabelValues(b.name).Inc()
		return nil, err
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: cancelled waiting for %s worker slot: %v", ErrRoutingTimeout, b.name, ctx.Err())
	}
	wait := time.Since(waitStart)
	if b.name == "internal" {
		middleware.RouteInternalQueueWaitDuration.Observe(wait.Seconds())
		middleware.RouteInternalActiveWorkers.Inc()
		defer middleware.RouteInternalActiveWorkers.Dec()
	}
	defer func() { <-b.sem }()

	return computeRouteResult(b.backend, ctx, mode, alternatives, startLat, startLng, endLat, endLng)
}

// ---- FallbackBackend -------------------------------------------------------

// FallbackBackend tries the primary backend first.
type FallbackBackend struct {
	primary   RouteBackend
	secondary RouteBackend
}

// NewFallbackBackend wraps two backends; primary is always tried first.
func NewFallbackBackend(primary, secondary RouteBackend) *FallbackBackend {
	return &FallbackBackend{primary: primary, secondary: secondary}
}

// BackendName returns the primary backend's name so cache keys reflect the
// intended routing strategy even when a fallback is used.
func (b *FallbackBackend) BackendName() string { return b.primary.BackendName() }

func (b *FallbackBackend) ComputeRoute(
	ctx context.Context,
	mode routing.TransportMode,
	alternatives int,
	startLat, startLng, endLat, endLng float64,
) (*model.RouteResponse, error) {
	result, err := b.ComputeRouteResult(ctx, mode, alternatives, startLat, startLng, endLat, endLng)
	if err != nil {
		return nil, err
	}
	return result.Response, nil
}

func (b *FallbackBackend) ComputeRouteResult(
	ctx context.Context,
	mode routing.TransportMode,
	alternatives int,
	startLat, startLng, endLat, endLng float64,
) (*RouteComputeResult, error) {
	resp, err := computeRouteResult(b.primary, ctx, mode, alternatives, startLat, startLng, endLat, endLng)
	if err == nil {
		return resp, nil
	}

	if ready, ok := b.secondary.(modeReadiness); ok && !ready.CanServeModeWithoutLoad(mode) {
		return nil, fmt.Errorf("%w: primary %s failed and secondary %s is not loaded: %v",
			ErrRoutingBackendUnavailable, b.primary.BackendName(), b.secondary.BackendName(), err)
	}

	fallbackResp, fallbackErr := computeRouteResult(b.secondary, ctx, mode, alternatives, startLat, startLng, endLat, endLng)
	if fallbackErr == nil {
		return fallbackResp, nil
	}
	if errors.Is(fallbackErr, ErrRoutingTimeout) ||
		errors.Is(fallbackErr, ErrRoutingOverloaded) ||
		errors.Is(fallbackErr, ErrRouteNotFound) ||
		errors.Is(fallbackErr, ErrRoutingBackendUnavailable) {
		return nil, fallbackErr
	}
	return nil, fmt.Errorf("%w: primary error: %v; fallback error: %v", ErrRoutingBackendUnavailable, err, fallbackErr)
}

// classifyRouteError returns the canonical metric label for a routing error.
// Used by metrics emitters across all backends.
func classifyRouteError(err error) string {
	if err == nil {
		return "success"
	}
	switch {
	case errors.Is(err, ErrRoutingOverloaded):
		return "overloaded"
	case errors.Is(err, ErrRoutingTimeout):
		return "timeout"
	case errors.Is(err, ErrRouteNotFound):
		return "not_found"
	case errors.Is(err, ErrRoutingBackendUnavailable):
		return "unavailable"
	default:
		return "error"
	}
}

func recordBackendAttempt(backend string, err error) {
	middleware.RouteBackendTotal.WithLabelValues(backend, classifyRouteError(err)).Inc()
}
