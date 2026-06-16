// Package main is the entry point for geo-service.
//
//	@title			geo-service
//	@version		1.0
//	@description	A production-ready geospatial routing microservice. Supports self-hosted OSRM (default) with an automatic in-process A* + Yen's k-shortest-paths fallback, live GPS tracking, WebSocket event delivery, and a PostGIS-backed OSM road graph.
//	@contact.name	GitHub Issues
//	@contact.url	https://github.com/Amirbehzad11/geo-service/issues
//	@license.name	MIT
//	@license.url	https://opensource.org/licenses/MIT
//	@host			localhost:8080
//	@BasePath		/
//	@schemes		http https
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	"geo-service/config"
	_ "geo-service/docs"
	"geo-service/internal/cache"
	"geo-service/internal/events"
	gpsapi "geo-service/internal/gps"
	"geo-service/internal/handler"
	"geo-service/internal/middleware"
	routeapi "geo-service/internal/route"
	"geo-service/internal/routing"
	"geo-service/internal/service"
	"geo-service/internal/storage"
	"geo-service/internal/ws"
	"geo-service/internal/wsnearby"
)

const (
	shipmentDBConnectAttemptTimeout = 10 * time.Second
	shipmentDBConnectRetryWindow    = 90 * time.Second
	shipmentDBConnectRetryDelay     = 3 * time.Second
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg := config.Load()

	redisClient := cache.NewRedis(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := redisClient.Ping(pingCtx); err != nil {
		log.Fatalf("redis: %v", err)
	}
	pingCancel()
	slog.Info("redis connected", "addr", cfg.RedisAddr)

	eventBus := events.NewBus(redisClient)
	hub := ws.NewHub(eventBus)

	pgCtx, pgCancel := context.WithTimeout(context.Background(), 10*time.Second)
	pg, err := storage.NewPostgres(pgCtx, cfg.PostgresDSN)
	pgCancel()
	if err != nil {
		log.Fatalf("postgres: %v", err)
	}
	if pg != nil {
		slog.Info("postgres connected (PostGIS enabled)")
	} else {
		slog.Info("postgres disabled — POSTGRES_DSN not set")
	}

	shipmentDB, err := connectShipmentDB(context.Background(), cfg)
	switch {
	case err != nil:
		// Shipment DB is optional — a connection failure disables the feature
		// but must not prevent the core routing service from starting.
		slog.Warn("shipment search disabled — could not connect to shipment database",
			"err", err,
		)
	case shipmentDB != nil:
		defer shipmentDB.Close()
		slog.Info("shipment database connected",
			"driver", cfg.ShipmentDBDriver,
			"table", cfg.ShipmentTable,
		)
	default:
		slog.Info("shipment search disabled — SHIPMENT_DB_DSN not set")
	}

	// ---- routing engine ---------------------------------------------------------
	internalEngine, internalLoader := prepareInternalRouting(cfg, pg)
	routeBackend := buildRouteBackend(cfg, internalEngine, internalLoader)

	// ---- services ---------------------------------------------------------------
	routeSvc := routeapi.NewRouteService(routeBackend, redisClient, eventBus, pg, routeapi.RouteServiceConfig{
		MaxInFlight:         cfg.RoutingMaxInFlight,
		QueueTimeoutMs:      cfg.RoutingQueueTimeoutMs,
		RouteTimeoutMs:      cfg.RoutingTimeoutMs,
		RouteCachePrecision: cfg.RouteCachePrecision,
		MaxAlternatives:     cfg.RoutingMaxAlternatives,
	})
	multiRouteSvc := routeapi.NewMultiRouteService(routeSvc)
	gpsSvc := gpsapi.NewGPSService(redisClient, eventBus, cfg.GPSRateLimitMs, cfg.DeviationThreshKm)
	driverSvc := service.NewDriverService(redisClient, cfg.DriverGeoKey, cfg.DriverLocationStreamKey, cfg.DriverSearchRadiusKm, cfg.ShipmentSearchLimit, shipmentDB)
	var shipmentSvc *service.ShipmentService
	if shipmentDB != nil {
		shipmentSvc = service.NewShipmentService(shipmentDB, cfg.ShipmentSearchRadiusKm, cfg.ShipmentSearchLimit)
	}

	// ---- handlers ---------------------------------------------------------------
	healthH := handler.NewHealthHandler(redisClient)
	routeH := routeapi.NewRouteHandler(routeSvc)
	multiRouteH := routeapi.NewMultiRouteHandler(multiRouteSvc)
	if shipmentDB != nil {
		routeH = routeapi.NewRouteHandler(routeSvc, shipmentDB)
		multiRouteH = routeapi.NewMultiRouteHandler(multiRouteSvc, shipmentDB)
	}
	gpsRequireAuth := cfg.JWTAuthEnabled || cfg.APIKeyEnabled
	gpsH := gpsapi.NewGPSHandler(gpsSvc, gpsRequireAuth)
	if shipmentDB != nil {
		gpsH = gpsapi.NewGPSHandler(gpsSvc, gpsRequireAuth, shipmentDB)
	}
	driverH := handler.NewDriverHandler(driverSvc)
	handler.ConfigureWebSocketOrigins(cfg.CORSAllowedOrigins)
	wsH := handler.NewWSHandler(hub, cfg.WebSocketTripAuthEnabled)
	if shipmentDB != nil {
		wsH = handler.NewWSHandler(hub, cfg.WebSocketTripAuthEnabled, shipmentDB)
	}
	shipmentWSH := handler.NewShipmentWSHandler(shipmentSvc, driverSvc, shipmentWSConfig(cfg), middleware.WSAuthOptions{
		RequireAuth:  cfg.WSShipmentAuthRequired,
		APIKey:       cfg.APIKey,
		JWTSecret:    cfg.JWTSecret,
		JWTAlgorithm: cfg.JWTAlgorithm,
	})

	// ---- background workers -----------------------------------------------------
	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()

	if pg != nil {
		batchWriter := storage.NewLocationBatchWriter(pg, eventBus, cfg.BatchSize, cfg.BatchFlushSec)
		go batchWriter.Run(workerCtx)
		slog.Info("location batch writer started",
			"batch_size", cfg.BatchSize,
			"flush_sec", cfg.BatchFlushSec,
		)
	}

	// ---- HTTP server ------------------------------------------------------------
	if os.Getenv("GIN_MODE") == "" {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	if err := r.SetTrustedProxies(nil); err != nil {
		log.Fatalf("gin trusted proxies: %v", err)
	}
	r.Use(
		middleware.CORS(cfg.CORSAllowedOrigins),
		middleware.StructuredLogger(),
		gin.Recovery(),
		middleware.RequestBodyLimit(cfg.RequestBodyLimit),
		middleware.IPRateLimit(cfg.RateLimitPerMin),
		middleware.RequestMetrics(),
	)
	if cfg.APIKeyEnabled && strings.TrimSpace(cfg.APIKey) == "" {
		log.Fatal("API_KEY_ENABLED=true requires API_KEY")
	}
	if cfg.JWTAuthEnabled && strings.TrimSpace(cfg.JWTSecret) == "" {
		log.Fatal("JWT_AUTH_ENABLED=true requires JWT_SECRET")
	}
	if cfg.APIKeyEnabled || cfg.JWTAuthEnabled {
		r.Use(middleware.Auth(middleware.AuthOptions{
			APIKey:       cfg.APIKey,
			JWTSecret:    cfg.JWTSecret,
			JWTAlgorithm: cfg.JWTAlgorithm,
		}))
		slog.Info("authentication enabled",
			"api_key", cfg.APIKeyEnabled,
			"jwt", cfg.JWTAuthEnabled,
			"jwt_alg", cfg.JWTAlgorithm,
		)
	} else {
		slog.Warn("authentication disabled; enable API_KEY_ENABLED or JWT_AUTH_ENABLED in production")
	}

	r.GET("/health", healthH.Check)
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))
	r.GET("/docs/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	r.POST("/route", routeH.Calculate)
	r.POST("/route/waypoints", multiRouteH.Calculate)
	r.POST("/driver-location", driverH.UpdateLocation)

	gps := r.Group("/gps")
	{
		gps.POST("/update", gpsH.Update)
		gps.GET("/trip/:id/location", gpsH.GetLocation)
	}

	r.GET("/ws/trip/:id", wsH.HandleConnection)
	r.GET("/ws/shipments/nearby", shipmentWSH.HandleNearby)

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		slog.Info("geo-service started", "port", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down...")
	workerCancel()

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Fatalf("graceful shutdown failed: %v", err)
	}

	if pg != nil {
		pg.Close()
	}
	slog.Info("stopped")
}

func connectShipmentDB(ctx context.Context, cfg *config.Config) (*storage.ShipmentDB, error) {
	return connectShipmentDBWithRetry(ctx, cfg, shipmentDBConnectRetryWindow, shipmentDBConnectRetryDelay)
}

func connectShipmentDBWithRetry(ctx context.Context, cfg *config.Config, retryWindow, retryDelay time.Duration) (*storage.ShipmentDB, error) {
	if cfg == nil || strings.TrimSpace(cfg.ShipmentDBDSN) == "" {
		return nil, nil
	}

	dbCfg := storage.ShipmentDBConfig{
		Driver:                 cfg.ShipmentDBDriver,
		DSN:                    cfg.ShipmentDBDSN,
		Table:                  cfg.ShipmentTable,
		LocationColumn:         cfg.ShipmentOriginLocationColumn,
		EndLocationColumn:      cfg.ShipmentEndLocationColumn,
		VehicleBoxSizesTable:   cfg.VehicleBoxSizesTable,
		VehicleIDColumn:        cfg.VehicleIDColumn,
		VehicleWeightColumn:    cfg.VehicleWeightColumn,
		VehicleTypesTable:      cfg.VehicleTypesTable,
		VehicleTypeLabelColumn: cfg.VehicleTypeLabelColumn,
		VehicleTypeTitleColumn: cfg.VehicleTypeTitleColumn,
		ContentTypesTable:      cfg.ContentTypesTable,
		ContentTypeIDColumn:    cfg.ContentTypeIDColumn,
		ContentTypeImageColumn: cfg.ContentTypeImageColumn,

		ShipmentImagesTable:           cfg.ShipmentImagesTable,
		ShipmentImageShipmentIDColumn: cfg.ShipmentImageShipmentIDColumn,
		ShipmentImageColumn:           cfg.ShipmentImageColumn,
	}

	deadline := time.Now().Add(retryWindow)
	var lastErr error
	for attempt := 1; ; attempt++ {
		attemptTimeout := shipmentDBConnectAttemptTimeout
		if remaining := time.Until(deadline); retryWindow > 0 && remaining < attemptTimeout {
			attemptTimeout = remaining
		}
		if attemptTimeout <= 0 {
			return nil, lastErr
		}

		attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		db, err := storage.NewShipmentDB(attemptCtx, dbCfg)
		cancel()
		if err == nil {
			return db, nil
		}
		lastErr = err

		if retryWindow <= 0 || retryDelay <= 0 || time.Now().Add(retryDelay).After(deadline) {
			return nil, lastErr
		}

		slog.Warn("shipment db connection failed; retrying",
			"attempt", attempt,
			"err", err,
			"retry_in_ms", retryDelay.Milliseconds(),
		)

		timer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func prepareInternalRouting(cfg *config.Config, pg *storage.Postgres) (*routing.Engine, func(context.Context) (*routing.Engine, error)) {
	loader := func(ctx context.Context) (*routing.Engine, error) {
		return newRoutingEngine(ctx, cfg, pg)
	}

	if !shouldLoadInternalGraphAtStartup(cfg) {
		slog.Info("internal road graph startup load skipped",
			"routing_backend", cfg.RoutingBackend,
			"internal_graph_enabled", cfg.InternalGraphEnabled,
			"internal_graph_lazy_load", cfg.InternalGraphLazyLoad,
		)
		return nil, loader
	}

	engine, err := loader(context.Background())
	if err != nil {
		if cfg.InternalGraphRequired {
			log.Fatalf("[routing] failed to load required road graph from PostGIS: %v", err)
		}
		slog.Warn("internal road graph unavailable; ground internal routes will return 503",
			"err", err,
		)
		return nil, loader
	}
	return engine, loader
}

func shouldLoadInternalGraphAtStartup(cfg *config.Config) bool {
	if !cfg.InternalGraphEnabled {
		return false
	}
	if strings.EqualFold(cfg.RoutingBackend, "osrm") {
		return false
	}
	return !cfg.InternalGraphLazyLoad
}

func newRoutingEngine(ctx context.Context, cfg *config.Config, pg *storage.Postgres) (*routing.Engine, error) {
	if pg == nil {
		return nil, fmt.Errorf("POSTGRES_DSN is required: PostGIS road graph unavailable")
	}

	timeout := graphLoadTimeout(cfg.RoadGraphLoadSec)

	roadGraph, err := loadGraphWithTimeout(ctx, timeout, func(c context.Context) (*routing.Graph, error) {
		return pg.LoadRoadGraphRegions(c, cfg.RoadGraphRegions)
	})
	if err != nil {
		return nil, err
	}
	slog.Info("road network loaded", "nodes", roadGraph.NodeCount(), "yen_spur_cap", cfg.RoutingYenSpurCap)
	engine := routing.NewEngineWithGraph(cfg.AvgSpeedKmH, roadGraph, cfg.RoutingYenSpurCap)

	// Rail graph is optional — missing data only disables train mode (returns 503).
	railGraph, err := loadGraphWithTimeout(ctx, timeout, func(c context.Context) (*routing.Graph, error) {
		return pg.LoadRailGraph(c)
	})
	if err != nil {
		slog.Warn("rail graph not loaded; train routing will be unavailable", "err", err)
	} else {
		engine.SetRailGraph(railGraph)
		slog.Info("rail network loaded", "nodes", railGraph.NodeCount())
	}

	// Transit overlay (bus + metro for supported cities) is optional too —
	// without it ModePublicTransport returns 503.
	transitGraph, err := loadGraphWithTimeout(ctx, timeout, func(c context.Context) (*routing.Graph, error) {
		return pg.LoadTransitGraph(c)
	})
	if err != nil {
		slog.Warn("transit graph not loaded; public-transport routing will be unavailable", "err", err)
	} else {
		engine.SetTransitGraph(transitGraph)
		slog.Info("transit network loaded", "nodes", transitGraph.NodeCount())
		// Per-edge polylines are computed lazily on first use (see
		// engine.ensureEdgePolyline) — startup-time enrichment was disabled
		// because warming all ~25k edges in parallel OOM'd the container on
		// memory-constrained hosts.
	}

	return engine, nil
}

func graphLoadTimeout(seconds int64) time.Duration {
	if seconds <= 0 {
		return 180 * time.Second
	}
	return time.Duration(seconds) * time.Second
}

func loadGraphWithTimeout(ctx context.Context, timeout time.Duration, load func(context.Context) (*routing.Graph, error)) (*routing.Graph, error) {
	loadCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return load(loadCtx)
}

func buildRouteBackend(cfg *config.Config, engine *routing.Engine, loader func(context.Context) (*routing.Engine, error)) routeapi.RouteBackend {
	queueTimeout := time.Duration(cfg.RoutingQueueTimeoutMs) * time.Millisecond
	internal := routeapi.NewInternalBackendWithConfig(routeapi.InternalBackendConfig{
		Engine:       engine,
		AvgSpeedKmH:  cfg.AvgSpeedKmH,
		GraphEnabled: cfg.InternalGraphEnabled,
		LazyLoad:     cfg.InternalGraphLazyLoad,
		LoadEngine:   loader,
	})
	internalLimited := routeapi.NewLimitedBackend(internal, cfg.InternalMaxInFlight, queueTimeout, "internal")

	if strings.EqualFold(cfg.RoutingBackend, "osrm") && cfg.OSRMBaseURL != "" {
		timeout := time.Duration(cfg.RoutingTimeoutMs) * time.Millisecond
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		osrmBackend := routeapi.NewOSRMBackend(cfg.OSRMBaseURL, timeout)
		osrmLimited := routeapi.NewLimitedBackend(osrmBackend, cfg.OSRMMaxInFlight, queueTimeout, "osrm")
		slog.Info("routing backend: OSRM primary",
			"osrm_url", cfg.OSRMBaseURL,
			"timeout_ms", cfg.RoutingTimeoutMs,
			"osrm_max_in_flight", cfg.OSRMMaxInFlight,
			"internal_graph_enabled", cfg.InternalGraphEnabled,
			"internal_graph_loaded", engine != nil && engine.HasGraph(),
		)
		return routeapi.NewFallbackBackend(osrmLimited, internalLimited)
	}

	slog.Info("routing backend: internal A* + Yen's k-shortest-paths",
		"total_max_in_flight", cfg.RoutingMaxInFlight,
		"internal_max_in_flight", cfg.InternalMaxInFlight,
		"internal_graph_loaded", engine != nil && engine.HasGraph(),
		"yen_spur_cap", cfg.RoutingYenSpurCap,
	)
	return internalLimited
}

func shipmentWSConfig(cfg *config.Config) wsnearby.Config {
	return wsnearby.Config{
		RequireTLS:      cfg.WSShipmentRequireTLS,
		RequireAuth:     cfg.WSShipmentAuthRequired,
		AllowLegacy:     cfg.WSShipmentLegacyFormat,
		MaxPayloadBytes: cfg.WSShipmentMaxPayloadBytes,
		MaxGlobalConns:  cfg.WSShipmentMaxConnections,
		MaxPerIP:        cfg.WSShipmentMaxPerIP,
		MessagesPerSec:  cfg.WSShipmentMessagesPerSec,
		MessageBurst:    cfg.WSShipmentMessageBurst,
		IdleTimeout:     time.Duration(cfg.WSShipmentIdleTimeoutSec) * time.Second,
		PingInterval:    time.Duration(cfg.WSShipmentPingIntervalSec) * time.Second,
		QueryTimeout:    10 * time.Second,
	}
}
