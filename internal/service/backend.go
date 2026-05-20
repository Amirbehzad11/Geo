package service

import (
	"context"
	"fmt"
	"time"

	"geo-service/internal/model"
	"geo-service/internal/osrmclient"
	"geo-service/internal/routing"
)

// RouteBackend computes a route and returns a RouteResponse.
// Implementations are free to call OSRM, the internal A* engine, or chain both.
type RouteBackend interface {
	// ComputeRoute performs the actual routing computation.
	// The ctx carries the deadline for the call; implementations must honour it.
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

// ---- InternalBackend -------------------------------------------------------

// InternalBackend wraps the in-process A* routing engine.
// It handles all transport modes including airplane (Bézier arc fallback).
type InternalBackend struct {
	engine *routing.Engine
}

// NewInternalBackend creates an InternalBackend backed by the given Engine.
func NewInternalBackend(engine *routing.Engine) *InternalBackend {
	return &InternalBackend{engine: engine}
}

func (b *InternalBackend) BackendName() string { return "internal" }

func (b *InternalBackend) ComputeRoute(
	ctx context.Context,
	mode routing.TransportMode,
	alternatives int,
	startLat, startLng, endLat, endLng float64,
) (*model.RouteResponse, error) {
	_ = ctx // internal engine is synchronous; ctx is checked only by the caller's semaphore
	routes := b.engine.CalculateAlternatives(startLat, startLng, endLat, endLng, mode, alternatives)
	if len(routes) == 0 {
		return nil, fmt.Errorf("internal engine: no route found")
	}
	return buildRouteResponse(mode, routes), nil
}

// ---- OSRMBackend -----------------------------------------------------------

// OSRMBackend delegates routing to a self-hosted OSRM instance.
// Modes that OSRM cannot serve (e.g. airplane) result in an error so that a
// FallbackBackend can cascade to the internal engine automatically.
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
	_ int, // OSRM single-route call; alternatives not used
	startLat, startLng, endLat, endLng float64,
) (*model.RouteResponse, error) {
	return b.client.Route(ctx, mode, startLat, startLng, endLat, endLng)
}

// ---- FallbackBackend -------------------------------------------------------

// FallbackBackend tries the primary backend first.
// On any error (including context deadline from OSRM, unsupported mode, network
// failure) it falls through to the secondary backend transparently.
type FallbackBackend struct {
	primary   RouteBackend
	secondary RouteBackend
}

// NewFallbackBackend wraps two backends; primary is always tried first.
func NewFallbackBackend(primary, secondary RouteBackend) *FallbackBackend {
	return &FallbackBackend{primary: primary, secondary: secondary}
}

// BackendName returns the primary backend's name so that cache keys reflect
// the intended routing strategy even when the secondary is used.
func (b *FallbackBackend) BackendName() string { return b.primary.BackendName() }

func (b *FallbackBackend) ComputeRoute(
	ctx context.Context,
	mode routing.TransportMode,
	alternatives int,
	startLat, startLng, endLat, endLng float64,
) (*model.RouteResponse, error) {
	resp, err := b.primary.ComputeRoute(ctx, mode, alternatives, startLat, startLng, endLat, endLng)
	if err == nil {
		return resp, nil
	}
	// Primary failed — cascade to secondary without exposing the primary error.
	return b.secondary.ComputeRoute(ctx, mode, alternatives, startLat, startLng, endLat, endLng)
}
