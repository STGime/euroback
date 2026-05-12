// Package main is the entrypoint for the Eurobase River worker process.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"time"

	"github.com/eurobase/euroback/internal/cron"
	"github.com/eurobase/euroback/internal/db"
	"github.com/eurobase/euroback/internal/functions"
	"github.com/eurobase/euroback/internal/storage"
	"github.com/eurobase/euroback/internal/workers"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

func main() {
	// ── Check for --migrate-only flag ──
	migrateOnly := len(os.Args) > 1 && os.Args[1] == "--migrate-only"

	// ── Load configuration from environment variables ──
	databaseURL := requireEnv("DATABASE_URL")

	// ── Set up structured logging ──
	logLevel := parseLogLevel(os.Getenv("LOG_LEVEL"))
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(logger)

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

	// ── Run River schema migrations ──
	slog.Info("running river schema migrations")
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		slog.Error("failed to create river migrator", "error", err)
		os.Exit(1)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		slog.Error("failed to run river migrations", "error", err)
		os.Exit(1)
	}
	slog.Info("river schema migrations complete")

	if migrateOnly {
		slog.Info("migrate-only mode, exiting")
		pool.Close()
		return
	}

	// ── Load remaining config ──
	s3Endpoint := requireEnv("S3_ENDPOINT")
	s3AccessKey := requireEnv("S3_ACCESS_KEY")
	s3SecretKey := requireEnv("S3_SECRET_KEY")
	_ = os.Getenv("REDIS_URL") // reserved for cache layer

	s3Region := os.Getenv("S3_REGION")
	if s3Region == "" {
		s3Region = "fr-par"
	}

	slog.Info("starting eurobase worker")

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
	river.AddWorker(riverWorkers, &workers.TenantExportWorker{
		DBPool: pool,
		S3:     s3Client,
	})
	river.AddWorker(riverWorkers, &workers.UserExportWorker{
		DBPool: pool,
		S3:     s3Client,
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

	// ── Start cron executor ──
	cronSvc := cron.NewCronService(pool)
	cronExec := cron.NewExecutor(cronSvc, pool)

	// Wire the function-runner invoker so schedules with action_type
	// `function` (issue #112) can fire deployed edge functions. Optional
	// — without it, function schedules fail-fast with a clear message,
	// SQL/RPC schedules keep working. Same env vars as the gateway.
	fnRunnerURL := os.Getenv("FUNCTION_RUNNER_URL")
	if fnRunnerURL != "" {
		var signer *functions.Signer
		if secret := os.Getenv("FUNCTIONS_RUNNER_HMAC_SECRET"); secret != "" {
			s, err := functions.NewSigner(secret)
			if err != nil {
				slog.Error("FUNCTIONS_RUNNER_HMAC_SECRET invalid for worker", "error", err)
				os.Exit(1)
			}
			signer = s
		}
		cronExec = cronExec.WithFunctionInvoker(cron.FunctionInvoker{
			RunnerURL: fnRunnerURL,
			Signer:    signer,
		})
		slog.Info("cron executor wired to functions runner", "url", fnRunnerURL, "signed", signer != nil)
	} else {
		slog.Warn("FUNCTION_RUNNER_URL not set on worker — `function` schedules will fail-fast")
	}
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := cronExec.RunDueJobs(ctx); err != nil {
					slog.Error("cron executor error", "error", err)
				}
			}
		}
	}()
	slog.Info("cron executor started (60s interval)")

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
