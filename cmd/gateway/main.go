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

	"github.com/af-corp/aegis-gateway/internal/audit"
	"github.com/af-corp/aegis-gateway/internal/auth"
	"github.com/af-corp/aegis-gateway/internal/config"
	"github.com/af-corp/aegis-gateway/internal/cost"
	"github.com/af-corp/aegis-gateway/internal/filter"
	"github.com/af-corp/aegis-gateway/internal/filter/injection"
	"github.com/af-corp/aegis-gateway/internal/filter/pii"
	"github.com/af-corp/aegis-gateway/internal/filter/policy"
	"github.com/af-corp/aegis-gateway/internal/filter/secrets"
	"github.com/af-corp/aegis-gateway/internal/gateway"
	"github.com/af-corp/aegis-gateway/internal/ratelimit"
	"github.com/af-corp/aegis-gateway/internal/retry"
	"github.com/af-corp/aegis-gateway/internal/router"
	"github.com/af-corp/aegis-gateway/internal/storage"
	"github.com/af-corp/aegis-gateway/internal/telemetry"
	"github.com/af-corp/aegis-gateway/internal/validation"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
)

var version = "dev"

func main() {
	configDir := flag.String("config", "configs", "path to configuration directory")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

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

	// Connect to PostgreSQL with pool configuration
	poolConfig, err := pgxpool.ParseConfig(cfg.Database.DSN())
	if err != nil {
		logger.Error("failed to parse database DSN", "error", err)
		os.Exit(1)
	}

	// Apply pool settings from config
	poolConfig.MaxConns = int32(cfg.Database.MaxOpenConns)
	poolConfig.MinConns = int32(cfg.Database.MaxIdleConns)
	poolConfig.MaxConnLifetime = cfg.Database.ConnMaxLifetime
	poolConfig.MaxConnIdleTime = 30 * time.Minute
	poolConfig.HealthCheckPeriod = 1 * time.Minute

	logger.Info("database pool configuration",
		"max_conns", poolConfig.MaxConns,
		"min_conns", poolConfig.MinConns,
		"max_conn_lifetime", poolConfig.MaxConnLifetime,
		"max_conn_idle_time", poolConfig.MaxConnIdleTime,
		"health_check_period", poolConfig.HealthCheckPeriod,
	)

	dbPool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		logger.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer dbPool.Close()

	if err := dbPool.Ping(context.Background()); err != nil {
		logger.Warn("database not reachable (gateway will start but auth will fail)", "error", err)
	} else {
		stats := dbPool.Stat()
		logger.Info("database connected",
			"acquired_conns", stats.AcquiredConns(),
			"idle_conns", stats.IdleConns(),
			"max_conns", stats.MaxConns(),
		)
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
		providerRegistry.ReplaceFrom(newRegistry)
		logger.Info("provider registry reloaded")
	})

	// Initialize metrics
	metrics := telemetry.NewMetrics()

	// Start metrics server
	metricsAddr := fmt.Sprintf(":%d", cfg.Telemetry.MetricsPort)
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())
	metricsSrv := &http.Server{Addr: metricsAddr, Handler: metricsMux}
	go func() {
		logger.Info("metrics server starting", "addr", metricsAddr)
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("metrics server error", "error", err)
		}
	}()

	// Start DB pool metrics collector
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			stats := dbPool.Stat()
			metrics.RecordDBPoolStats(
				stats.AcquiredConns(),
				stats.IdleConns(),
				stats.MaxConns(),
				stats.TotalConns(),
			)
		}
	}()

	// Build filter chain
	secretsFilter := secrets.NewFilter(func() bool { return loader.Config().Filter.Secrets.Enabled })
	injectionScanner := injection.NewScanner(func() config.InjectionFilterConfig { return loader.Config().Filter.Injection })
	piiClient := pii.NewClient(func() config.PIIServiceConfig { return loader.Config().Filter.PIIService })
	if cfg.Filter.PIIService.Enabled {
		if err := piiClient.Connect(); err != nil {
			logger.Warn("failed to connect to PII service", "error", err)
		}
	}
	policyEvaluator := policy.NewEvaluator(func() config.PolicyFilterConfig { return loader.Config().Filter.Policy })
	policyEvaluator.SetMetrics(metrics)
	if cfg.Filter.Policy.Enabled {
		if err := policyEvaluator.Load(); err != nil {
			logger.Warn("failed to load OPA policies (policy filter disabled)", "error", err)
		}
	}
	loader.OnReload(func() {
		if err := policyEvaluator.Load(); err != nil {
			logger.Error("policy reload failed", "error", err)
		} else {
			logger.Info("policies reloaded")
		}
	})
	filterChain := filter.NewChain(secretsFilter, injectionScanner, piiClient)

	// Rate limiting
	rateLimiter := ratelimit.NewLimiter(rdb)
	budgetTracker := ratelimit.NewBudgetTracker(rdb)

	// Health tracking (circuit breaker)
	healthTracker := router.NewHealthTracker(
		cfg.Routing.CircuitBreaker.FailureThreshold,
		cfg.Routing.CircuitBreaker.RecoveryProbeInterval,
	)

	// Build audit logger
	auditLogger := audit.NewLogger(dbPool)

	// Build retry executor
	retryConfig := retry.Config{
		MaxRetries:        cfg.Routing.MaxRetries,
		InitialBackoff:    100 * time.Millisecond,
		MaxBackoff:        5 * time.Second,
		BackoffMultiplier: 2.0,
		JitterFraction:    0.1,
	}
	retryExecutor := retry.NewExecutor(retryConfig, metrics)
	contextMonitor := retry.NewContextMonitor(metrics)

	// Build input validator
	validator := validation.NewValidator(validation.DefaultLimits(), metrics)

	// Build handler
	keyStore := auth.NewCachedKeyStore(dbPool, rdb)
	costCalc := cost.NewCalculator(func() *config.ModelsConfig {
		return loader.Models()
	})
	usageRecorder := storage.NewUsageRecorder(dbPool)
	handler := gateway.NewHandler(providerRegistry, healthTracker, func() *config.ModelsConfig {
		return loader.Models()
	}, func() *config.Config {
		return loader.Config()
	}, filterChain, policyEvaluator, metrics, costCalc, usageRecorder, auditLogger, retryExecutor, contextMonitor, validator)

	// Router setup
	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(requestIDMiddleware)

	// Unauthenticated routes
	r.Get("/aegis/v1/health", makeHealthHandler(dbPool, rdb, rateLimiter, providerRegistry, healthTracker))

	// Authenticated routes
	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware(keyStore, auditLogger))
		r.Use(ratelimit.Middleware(rateLimiter, budgetTracker, metrics, auditLogger))
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

// Health check response structure
type healthResponse struct {
	Status    string           `json:"status"`
	Version   string           `json:"version"`
	Timestamp time.Time        `json:"timestamp"`
	Database  *databaseHealth  `json:"database,omitempty"`
	Redis     *redisHealth     `json:"redis,omitempty"`
	Providers *providersHealth `json:"providers,omitempty"`
}

type databaseHealth struct {
	Connected     bool  `json:"connected"`
	AcquiredConns int32 `json:"acquired_conns"`
	IdleConns     int32 `json:"idle_conns"`
	MaxConns      int32 `json:"max_conns"`
	TotalConns    int32 `json:"total_conns"`
	Latency       int64 `json:"latency_ms,omitempty"`
}

type redisHealth struct {
	Connected      bool   `json:"connected"`
	CircuitBreaker string `json:"circuit_breaker,omitempty"`
	Latency        int64  `json:"latency_ms,omitempty"`
}

type providersHealth struct {
	Available int                       `json:"available"`
	Total     int                       `json:"total"`
	Details   map[string]providerStatus `json:"details,omitempty"`
}

type providerStatus struct {
	Healthy bool   `json:"healthy"`
	State   string `json:"state,omitempty"`
}

func makeHealthHandler(pool *pgxpool.Pool, rdb *redis.Client, limiter *ratelimit.Limiter, registry *router.Registry, healthTracker *router.HealthTracker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := healthResponse{
			Status:    "healthy",
			Version:   version,
			Timestamp: time.Now(),
		}

		// Check database connectivity
		if pool != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()

			dbHealth := &databaseHealth{}
			start := time.Now()
			if err := pool.Ping(ctx); err != nil {
				dbHealth.Connected = false
				resp.Status = "degraded"
			} else {
				dbHealth.Connected = true
				dbHealth.Latency = time.Since(start).Milliseconds()
				stats := pool.Stat()
				dbHealth.AcquiredConns = stats.AcquiredConns()
				dbHealth.IdleConns = stats.IdleConns()
				dbHealth.MaxConns = stats.MaxConns()
				dbHealth.TotalConns = stats.TotalConns()
			}
			resp.Database = dbHealth
		}

		// Check Redis connectivity and circuit breaker state
		if rdb != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()

			redisHealth := &redisHealth{}
			start := time.Now()
			if err := rdb.Ping(ctx).Err(); err != nil {
				redisHealth.Connected = false
				resp.Status = "degraded"
			} else {
				redisHealth.Connected = true
				redisHealth.Latency = time.Since(start).Milliseconds()
			}

			// Get circuit breaker state
			if limiter != nil {
				cbState := limiter.GetCircuitBreakerState()
				redisHealth.CircuitBreaker = cbState
				if cbState == "open" {
					resp.Status = "degraded"
				}
			}
			resp.Redis = redisHealth
		}

		// Check provider availability using health tracker
		if registry != nil && healthTracker != nil {
			provHealth := &providersHealth{
				Details: make(map[string]providerStatus),
			}

			providers := registry.ListProviders()
			provHealth.Total = len(providers)

			for _, provName := range providers {
				healthy := healthTracker.IsAvailable(provName)
				state := healthTracker.GetState(provName)
				
				if healthy {
					provHealth.Available++
				}
				
				provHealth.Details[provName] = providerStatus{
					Healthy: healthy,
					State:   state,
				}
			}

			// If no providers are available, mark as unhealthy
			if provHealth.Available == 0 && provHealth.Total > 0 {
				resp.Status = "unhealthy"
			}

			resp.Providers = provHealth
		}

		w.Header().Set("Content-Type", "application/json")
		switch resp.Status {
		case "unhealthy":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "degraded":
			// Still return 200 for degraded but include status in body
		}
		_ = json.NewEncoder(w).Encode(resp)
	}
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
	_, _ = rand.Read(b)
	return fmt.Sprintf("req_%d_%s", now.UnixMilli(), hex.EncodeToString(b))
}
