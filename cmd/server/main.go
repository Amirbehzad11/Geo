// Package main is the entry point for geo-service.
//
//	@title			geo-service
//	@version		1.0
//	@description	A production-ready geospatial routing microservice with a custom A* + Yen's k-shortest-paths algorithm, live GPS tracking, WebSocket event delivery, and a PostGIS-backed OSM road graph.
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
	ginSwagger "github.com/swaggo/gin-swagger"
	swaggerFiles "github.com/swaggo/files"

	"geo-service/config"
	"geo-service/internal/cache"
	_ "geo-service/docs"
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

	routingEngine := newRoutingEngine(cfg, pg)

	routeSvc := service.NewRouteService(routingEngine, redisClient, eventBus, pg)
	gpsSvc := service.NewGPSService(redisClient, eventBus, cfg.GPSRateLimitMs, cfg.DeviationThreshKm)

	healthH := handler.NewHealthHandler(redisClient)
	routeH := handler.NewRouteHandler(routeSvc)
	gpsH := handler.NewGPSHandler(gpsSvc)
	wsH := handler.NewWSHandler(hub)

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

	gps := r.Group("/gps")
	{
		gps.POST("/update", gpsH.Update)
		gps.GET("/trip/:id/location", gpsH.GetLocation)
	}

	r.GET("/ws/trip/:id", wsH.HandleConnection)

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
