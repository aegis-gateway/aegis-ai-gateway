package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/af-corp/aegis-gateway/internal/auth"
	"github.com/af-corp/aegis-gateway/internal/config"
	"github.com/af-corp/aegis-gateway/internal/gateway"
	"github.com/af-corp/aegis-gateway/internal/router"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

var version = "dev"

func main() {
	configDir := flag.String("config", "configs", "path to configuration directory")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// Load configuration
	loader := config.NewLoader(*configDir, logger)
	if err := loader.Load(); err != nil {
		logger.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	if err := loader.Watch(); err != nil {
		logger.Warn("failed to start config watcher", "error", err)
	}

	cfg := loader.Config()

	// Connect to PostgreSQL
	dbPool, err := pgxpool.New(context.Background(), cfg.Database.DSN())
	if err != nil {
		logger.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer dbPool.Close()

	if err := dbPool.Ping(context.Background()); err != nil {
		logger.Warn("database not reachable (gateway will start but auth will fail)", "error", err)
	} else {
		logger.Info("database connected")
	}

	// Connect to Redis
	var rdb *redis.Client
	if len(cfg.Redis.Addresses) > 0 && cfg.Redis.Addresses[0] != "" {
		rdb = redis.NewClient(&redis.Options{
			Addr:     cfg.Redis.Addresses[0],
			Password: cfg.Redis.Password,
			DB:       cfg.Redis.DB,
			PoolSize: cfg.Redis.PoolSize,
		})
		if err := rdb.Ping(context.Background()).Err(); err != nil {
			logger.Warn("redis not reachable (auth cache disabled)", "error", err)
			rdb = nil
		} else {
			logger.Info("redis connected")
		}
	}

	// Build provider registry
	providerRegistry := router.BuildFromConfig(loader.Providers())
	loader.OnReload(func() {
		newRegistry := router.BuildFromConfig(loader.Providers())
		*providerRegistry = *newRegistry
		logger.Info("provider registry reloaded")
	})

	// Build handler
	keyStore := auth.NewCachedKeyStore(dbPool, rdb)
	handler := gateway.NewHandler(providerRegistry, func() *config.ModelsConfig {
		return loader.Models()
	})

	// Router setup
	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(requestIDMiddleware)

	// Unauthenticated routes
	r.Get("/aegis/v1/health", healthHandler)

	// Authenticated routes
	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware(keyStore))
		r.Post("/v1/chat/completions", handler.ChatCompletions)
		r.Get("/v1/models", handler.ListModels)
	})

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	// Graceful shutdown
	errCh := make(chan error, 1)
	go func() {
		logger.Info("gateway starting", "addr", addr, "version", version)
		errCh <- srv.ListenAndServe()
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-quit:
		logger.Info("received shutdown signal", "signal", sig)
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Server.GracefulShutdown)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}
	logger.Info("gateway stopped")
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "healthy",
		"version": version,
	})
}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" {
			reqID = generateRequestID()
		}
		w.Header().Set("X-Request-ID", reqID)
		ctx := context.WithValue(r.Context(), requestIDKey, reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type contextKey string

const requestIDKey contextKey = "request_id"

func generateRequestID() string {
	now := time.Now()
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("req_%d_%s", now.UnixMilli(), hex.EncodeToString(b))
}
