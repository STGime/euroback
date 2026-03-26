// Package main is the entrypoint for the Eurobase API gateway.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/db"
	"github.com/eurobase/euroback/internal/email"
	"github.com/eurobase/euroback/internal/gateway"
	"github.com/eurobase/euroback/internal/ratelimit"
	"github.com/eurobase/euroback/internal/realtime"
	"github.com/eurobase/euroback/internal/storage"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	// ── Check for --create-admin flag ──
	if len(os.Args) >= 2 && os.Args[1] == "--create-admin" {
		createAdmin()
		return
	}

	// ── Load configuration from environment variables ──
	databaseURL := requireEnv("DATABASE_URL")
	platformJWTSecret := requireEnv("PLATFORM_JWT_SECRET")
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

	// ── Set up platform auth ──
	platformAuthSvc := auth.NewPlatformAuthService(pool, platformJWTSecret)
	platformAuth := auth.NewPlatformAuthMiddleware(platformAuthSvc)

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

	// ── Set up Scaleway TEM email client (optional — degrades gracefully) ──
	scwTEMKey := os.Getenv("SCW_TEM_SECRET_KEY")
	scwTEMRegion := os.Getenv("SCW_TEM_REGION")
	scwTEMProjectID := os.Getenv("SCW_TEM_PROJECT_ID")
	if scwTEMProjectID == "" {
		scwTEMProjectID = os.Getenv("SCW_PROJECT_ID") // fallback to main project ID
	}
	emailFromAddr := os.Getenv("EMAIL_FROM_ADDRESS")
	emailFromName := os.Getenv("EMAIL_FROM_NAME")
	consoleURL := os.Getenv("CONSOLE_URL")
	if consoleURL == "" {
		consoleURL = "http://localhost:5173"
	}

	emailClient := email.NewEmailClient(scwTEMKey, scwTEMRegion, scwTEMProjectID, emailFromAddr, emailFromName)
	var emailService *email.EmailService
	if emailClient.Configured() {
		emailService = email.NewEmailService(emailClient, pool, consoleURL)
		slog.Info("email service configured (Scaleway TEM)")
	} else {
		emailService = email.NewEmailService(emailClient, pool, consoleURL)
		slog.Warn("SCW_TEM_SECRET_KEY or EMAIL_FROM_ADDRESS not set, emails will be logged instead of sent")
	}

	// Wire email into platform auth.
	platformAuthSvc.SetEmailService(emailService)

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
	_ = rtBridge // Available for cross-instance fan-out via EventPublisher.

	// ── Set up request log pipeline ──
	logCh := make(chan gateway.LogEntry, 10000)
	gateway.StartLogWriter(ctx, pool, logCh)
	gateway.StartLogCleanup(ctx, pool)
	slog.Info("request logging pipeline started")

	// ── Dev mode: bypass platform auth for local testing ──
	devMode := os.Getenv("DEV_MODE") == "true"

	// ── Set up subdomain middleware for SDK URLs ({slug}.eurobase.app) ──
	domainSuffix := os.Getenv("DOMAIN_SUFFIX")
	if domainSuffix == "" {
		domainSuffix = "eurobase.app"
	}
	subdomainMw := auth.NewSubdomainMiddleware(pool, domainSuffix)

	// ── Set up chi router (extracted for testability) ──
	r := gateway.NewRouter(pool, platformAuth, platformAuthSvc, limiter, s3Client, hub, logCh, subdomainMw, emailService, devMode)

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

// createAdmin creates an initial admin platform user.
// Usage: go run ./cmd/gateway --create-admin email password
func createAdmin() {
	if len(os.Args) < 4 {
		fmt.Fprintf(os.Stderr, "Usage: %s --create-admin <email> <password>\n", os.Args[0])
		os.Exit(1)
	}

	email := os.Args[2]
	password := os.Args[3]

	databaseURL := requireEnv("DATABASE_URL")

	ctx := context.Background()
	pool, err := db.NewPool(ctx, databaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect to database: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to hash password: %v\n", err)
		os.Exit(1)
	}

	var userID string
	err = pool.QueryRow(ctx,
		`INSERT INTO platform_users (email, password_hash, email_confirmed_at)
		 VALUES ($1, $2, now())
		 ON CONFLICT (email) WHERE email != ''
		 DO UPDATE SET password_hash = EXCLUDED.password_hash
		 RETURNING id`,
		email, string(hash),
	).Scan(&userID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create admin user: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Admin user created: %s (id: %s)\n", email, userID)
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
