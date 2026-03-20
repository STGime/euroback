// Package main is the entrypoint for the Eurobase API gateway.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/db"
	"github.com/eurobase/euroback/internal/gateway"
	"github.com/eurobase/euroback/internal/ratelimit"
	"github.com/eurobase/euroback/internal/realtime"
	"github.com/eurobase/euroback/internal/storage"
)

func main() {
	// ── Load configuration from environment variables ──
	databaseURL := requireEnv("DATABASE_URL")
	hankoAPIURL := requireEnv("HANKO_API_URL")
	hankoWebhookSecret := requireEnv("HANKO_WEBHOOK_SECRET")
	redisURL := os.Getenv("REDIS_URL")

	port := os.Getenv("GATEWAY_PORT")
	if port == "" {
		port = "8080"
	}

	// ── Set up structured logging ──
	logLevel := parseLogLevel(os.Getenv("LOG_LEVEL"))
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(logger)

	slog.Info("starting eurobase gateway", "port", port)

	// ── Initialize database connection pool ──
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := db.NewPool(ctx, databaseURL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	slog.Info("database connection pool established")

	// ── Set up auth middleware ──
	hankoAuth := auth.NewHankoMiddleware(hankoAPIURL)

	// ── Set up rate limiter (optional — degrades gracefully) ──
	var limiter *ratelimit.RateLimiter
	if redisURL != "" {
		var err error
		limiter, err = ratelimit.NewRateLimiter(redisURL)
		if err != nil {
			slog.Warn("failed to connect rate limiter to redis, rate limiting disabled", "error", err)
		} else {
			defer limiter.Close()
		}
	} else {
		slog.Warn("REDIS_URL not set, rate limiting disabled")
	}

	// ── Set up S3 client for Scaleway Object Storage (optional — degrades gracefully) ──
	var s3Client *storage.S3Client
	scwAccessKey := os.Getenv("SCW_ACCESS_KEY")
	scwSecretKey := os.Getenv("SCW_SECRET_KEY")
	scwS3Endpoint := os.Getenv("SCW_S3_ENDPOINT")
	scwS3Region := os.Getenv("SCW_S3_REGION")
	if scwAccessKey != "" && scwSecretKey != "" {
		if scwS3Endpoint == "" {
			scwS3Endpoint = "https://s3.fr-par.scw.cloud"
		}
		var s3Err error
		s3Client, s3Err = storage.NewS3Client(scwS3Endpoint, scwS3Region, scwAccessKey, scwSecretKey)
		if s3Err != nil {
			slog.Error("failed to create S3 client, storage routes will be disabled", "error", s3Err)
		}
	} else {
		slog.Warn("SCW_ACCESS_KEY / SCW_SECRET_KEY not set, storage routes disabled")
	}

	// ── Set up realtime WebSocket hub ──
	hub := realtime.NewHub()
	go hub.Run()
	slog.Info("realtime hub started")

	// Set up Redis bridge for cross-instance realtime fan-out (optional).
	var rtBridge *realtime.RedisBridge
	if redisURL != "" {
		var bridgeErr error
		rtBridge, bridgeErr = realtime.NewRedisBridge(redisURL, hub)
		if bridgeErr != nil {
			slog.Warn("failed to connect realtime redis bridge, cross-instance fan-out disabled",
				"error", bridgeErr,
			)
		} else {
			go rtBridge.Subscribe(ctx)
			defer rtBridge.Close()
		}
	} else {
		slog.Warn("REDIS_URL not set, realtime cross-instance fan-out disabled")
	}
	_ = rtBridge // EventPublisher will use this when integrated with the query engine.

	// ── Dev mode: bypass Hanko auth for local testing ──
	devMode := os.Getenv("DEV_MODE") == "true"

	// ── Set up chi router (extracted for testability) ──
	r := gateway.NewRouter(pool, hankoAuth, hankoWebhookSecret, limiter, s3Client, hub, devMode)

	// ── Start HTTP server ──
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// ── Graceful shutdown ──
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		slog.Info("gateway listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	sig := <-sigCh
	slog.Info("received shutdown signal", "signal", sig.String())

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}

	pool.Close()
	slog.Info("gateway shut down cleanly")
}

// requireEnv reads a required environment variable or exits with an error.
func requireEnv(key string) string {
	val := os.Getenv(key)
	if val == "" {
		slog.Error("required environment variable not set", "key", key)
		os.Exit(1)
	}
	return val
}

// parseLogLevel converts a string log level to slog.Level.
func parseLogLevel(level string) slog.Level {
	switch level {
	case "DEBUG", "debug":
		return slog.LevelDebug
	case "WARN", "warn":
		return slog.LevelWarn
	case "ERROR", "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
