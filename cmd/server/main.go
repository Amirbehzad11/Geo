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
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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
	"geo-service/internal/handler"
	"geo-service/internal/middleware"
	"geo-service/internal/routing"
	"geo-service/internal/service"
	"geo-service/internal/storage"
	"geo-service/internal/ws"
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

	shipmentCtx, shipmentCancel := context.WithTimeout(context.Background(), 10*time.Second)
	shipmentDB, err := storage.NewShipmentDB(shipmentCtx, storage.ShipmentDBConfig{
		Driver:    cfg.ShipmentDBDriver,
		DSN:       cfg.ShipmentDBDSN,
		Table:     cfg.ShipmentTable,
		LatColumn: cfg.ShipmentOriginLatColumn,
		LngColumn: cfg.ShipmentOriginLngColumn,
	})
	shipmentCancel()
	if err != nil {
		log.Fatalf("shipment database: %v", err)
	}
	if shipmentDB != nil {
		defer shipmentDB.Close()
		slog.Info("shipment database connected",
			"driver", cfg.ShipmentDBDriver,
			"table", cfg.ShipmentTable,
		)
	} else {
		slog.Info("shipment search disabled — SHIPMENT_DB_DSN not set")
	}

	// ---- routing engine ---------------------------------------------------------
	// The internal A* engine is always initialised (used as the fallback and for
	// airplane mode which OSRM does not support).
	internalEngine := newRoutingEngine(cfg, pg)
	routeBackend := buildRouteBackend(cfg, internalEngine)

	// ---- services ---------------------------------------------------------------
	routeSvc := service.NewRouteService(routeBackend, redisClient, eventBus, pg, service.RouteServiceConfig{
		MaxInFlight:    cfg.RoutingMaxInFlight,
		QueueTimeoutMs: cfg.RoutingQueueTimeoutMs,
		RouteTimeoutMs: cfg.RoutingTimeoutMs,
	})
	gpsSvc := service.NewGPSService(redisClient, eventBus, cfg.GPSRateLimitMs, cfg.DeviationThreshKm)
	driverSvc := service.NewDriverService(redisClient, cfg.DriverGeoKey, cfg.DriverLocationStreamKey, cfg.DriverSearchRadiusKm, cfg.ShipmentSearchLimit)
	var shipmentSvc *service.ShipmentService
	if shipmentDB != nil {
		shipmentSvc = service.NewShipmentService(shipmentDB, cfg.ShipmentSearchRadiusKm, cfg.ShipmentSearchLimit)
	}

	// ---- handlers ---------------------------------------------------------------
	healthH := handler.NewHealthHandler(redisClient)
	routeH := handler.NewRouteHandler(routeSvc)
	gpsH := handler.NewGPSHandler(gpsSvc)
	driverH := handler.NewDriverHandler(driverSvc)
	wsH := handler.NewWSHandler(hub)
	shipmentWSH := handler.NewShipmentWSHandler(shipmentSvc, driverSvc)

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
	r.Use(
		middleware.CORS(cfg.CORSAllowedOrigins),
		middleware.StructuredLogger(),
		gin.Recovery(),
		middleware.RequestMetrics(),
	)
	if cfg.APIKeyEnabled && cfg.APIKey != "" {
		r.Use(middleware.APIKeyAuth(cfg.APIKey))
		slog.Info("API key authentication enabled")
	}

	r.GET("/health", healthH.Check)
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))
	r.GET("/docs/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	r.POST("/route", routeH.Calculate)
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

	pg.Close()
	slog.Info("stopped")
}

// newRoutingEngine loads the road graph from PostGIS and returns an Engine.
// The internal engine is always created — it is used as the fallback when OSRM
// is unavailable, and exclusively for airplane mode in all configurations.
func newRoutingEngine(cfg *config.Config, pg *storage.Postgres) *routing.Engine {
	if pg == nil {
		log.Fatal("[routing] POSTGRES_DSN is required — PostGIS road graph unavailable")
	}

	timeout := time.Duration(cfg.RoadGraphLoadSec) * time.Second
	if timeout <= 0 {
		timeout = 180 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	g, err := pg.LoadRoadGraphRegions(ctx, cfg.RoadGraphRegions)
	cancel()
	if err != nil {
		log.Fatalf("[routing] failed to load road graph from PostGIS: %v", err)
	}

	slog.Info("road network loaded", "nodes", g.NodeCount())
	return routing.NewEngineWithGraph(cfg.AvgSpeedKmH, g)
}

// buildRouteBackend constructs the active RouteBackend based on config.
//
//   - ROUTING_BACKEND=internal  →  InternalBackend only
//   - ROUTING_BACKEND=osrm      →  OSRMBackend with InternalBackend as fallback
//
// The internal engine is always available as the fallback so that airplane
// mode and remote-area endpoints (outside OSRM graph coverage) still work.
func buildRouteBackend(cfg *config.Config, engine *routing.Engine) service.RouteBackend {
	internal := service.NewInternalBackend(engine)

	if cfg.RoutingBackend == "osrm" && cfg.OSRMBaseURL != "" {
		timeout := time.Duration(cfg.RoutingTimeoutMs) * time.Millisecond
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		osrmBackend := service.NewOSRMBackend(cfg.OSRMBaseURL, timeout)
		slog.Info("routing backend: OSRM with internal fallback",
			"osrm_url", cfg.OSRMBaseURL,
			"timeout_ms", cfg.RoutingTimeoutMs,
		)
		return service.NewFallbackBackend(osrmBackend, internal)
	}

	slog.Info("routing backend: internal A* + Yen's k-shortest-paths",
		"max_in_flight", cfg.RoutingMaxInFlight,
	)
	return internal
}
