// Package main is the entrypoint for the Eurobase River worker process.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/eurobase/euroback/internal/db"
	"github.com/eurobase/euroback/internal/storage"
	"github.com/eurobase/euroback/internal/workers"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

func main() {
	// ── Load configuration from environment variables ──
	databaseURL := requireEnv("DATABASE_URL")
	s3Endpoint := requireEnv("S3_ENDPOINT")
	s3AccessKey := requireEnv("S3_ACCESS_KEY")
	s3SecretKey := requireEnv("S3_SECRET_KEY")
	_ = os.Getenv("REDIS_URL") // reserved for cache layer

	s3Region := os.Getenv("S3_REGION")
	if s3Region == "" {
		s3Region = "fr-par"
	}

	// ── Set up structured logging ──
	logLevel := parseLogLevel(os.Getenv("LOG_LEVEL"))
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(logger)

	slog.Info("starting eurobase worker")

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

	// ── Initialize Scaleway S3 client ──
	s3Client, err := storage.NewS3Client(s3Endpoint, s3Region, s3AccessKey, s3SecretKey)
	if err != nil {
		slog.Error("failed to initialize s3 client", "error", err)
		os.Exit(1)
	}
	slog.Info("s3 client initialized")

	// ── Register River workers ──
	riverWorkers := river.NewWorkers()
	river.AddWorker(riverWorkers, &workers.ProvisionProjectWorker{
		S3:     s3Client,
		DBPool: pool,
	})

	// ── Create River client in worker mode ──
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: 10},
		},
		Workers: riverWorkers,
	})
	if err != nil {
		slog.Error("failed to create river client", "error", err)
		os.Exit(1)
	}

	// ── Start River client ──
	if err := riverClient.Start(ctx); err != nil {
		slog.Error("failed to start river client", "error", err)
		os.Exit(1)
	}
	slog.Info("river worker started")

	// ── Graceful shutdown ──
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigCh
	slog.Info("received shutdown signal", "signal", sig.String())

	cancel()

	if err := riverClient.Stop(context.Background()); err != nil {
		slog.Error("failed to stop river client gracefully", "error", err)
		os.Exit(1)
	}

	pool.Close()
	slog.Info("worker shut down cleanly")
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
