// Package main is the entrypoint for the Eurobase API gateway.
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

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/db"
	"github.com/eurobase/euroback/internal/email"
	"github.com/eurobase/euroback/internal/gateway"
	"github.com/eurobase/euroback/internal/plans"
	"github.com/eurobase/euroback/internal/ratelimit"
	"github.com/eurobase/euroback/internal/realtime"
	"github.com/eurobase/euroback/internal/sms"
	"github.com/eurobase/euroback/internal/storage"
	"github.com/eurobase/euroback/internal/tenant"
	"github.com/eurobase/euroback/internal/vault"
	"github.com/jackc/pgx/v5/pgxpool"
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

	// Optional second pool for platform-authenticated developer traffic
	// (DATABASE_URL_DEVELOPER → eurobase_developer role, member of
	// eurobase_migrator). Wired only into routes under /platform that
	// run developer-authored SQL/DDL on tenant schemas. SDK runtime
	// traffic stays on the gateway pool above. If the env var is empty
	// (local dev before bootstrap, or staging without the role) we leave
	// developerPool nil and the router falls back to the gateway pool —
	// migrations that need ALTER/REFERENCES will still fail with a clear
	// error from the SET ROLE attempt.
	developerDatabaseURL := os.Getenv("DATABASE_URL_DEVELOPER")
	var developerPool *pgxpool.Pool
	if developerDatabaseURL != "" {
		developerPool, err = db.NewPool(ctx, developerDatabaseURL)
		if err != nil {
			slog.Error("failed to connect with DATABASE_URL_DEVELOPER", "error", err)
			os.Exit(1)
		}
		defer developerPool.Close()
		slog.Info("developer database connection pool established")
	} else {
		slog.Warn("DATABASE_URL_DEVELOPER not set — platform routes will run on the gateway pool and will fail on tenant DDL until the developer role is configured")
	}

	// ── Set up plan limits ──
	limitsSvc := plans.NewLimitsService(pool)
	slog.Info("plan limits service initialized")

	// ── Set up platform auth ──
	platformAuthSvc := auth.NewPlatformAuthService(pool, platformJWTSecret)
	platformAuthSvc.AllowPublicSignup = os.Getenv("ALLOW_PUBLIC_SIGNUP") == "true"
	if !platformAuthSvc.AllowPublicSignup {
		slog.Info("signup gated behind platform_allowlist (set ALLOW_PUBLIC_SIGNUP=true to open)")
	}
	patSvc := auth.NewPATService(pool)
	platformAuth := auth.NewPlatformAuthMiddleware(platformAuthSvc).WithPATService(patSvc)

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

	// ── Set up GatewayAPI SMS client (optional — degrades gracefully) ──
	smsAPIToken := os.Getenv("GATEWAYAPI_TOKEN")
	smsSender := os.Getenv("SMS_SENDER")
	smsClient := sms.NewClient(smsAPIToken, smsSender)
	var smsService *sms.Service
	if smsClient.Configured() {
		smsService = sms.NewService(smsClient, pool)
		slog.Info("sms service configured (GatewayAPI)")
	} else {
		smsService = sms.NewService(smsClient, pool)
		slog.Warn("GATEWAYAPI_TOKEN not set, SMS will be logged instead of sent")
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
	_ = rtBridge // Available for cross-instance fan-out via EventPublisher.

	// ── Set up request log pipeline ──
	logCh := make(chan gateway.LogEntry, 10000)
	gateway.StartLogWriter(ctx, pool, logCh)
	gateway.StartLogCleanup(ctx, pool)
	slog.Info("request logging pipeline started")

	// ── Start token cleanup job (expired refresh/email tokens across all tenants) ──
	gateway.StartTokenCleanup(ctx, pool)
	slog.Info("token cleanup job started")

	// ── Start usage alerts job (daily scan, 80/90/100% thresholds) ──
	limitsSvc.StartUsageAlerts(ctx, emailService)
	slog.Info("usage alerts job started")

	// ── Dev mode: bypass platform auth for local testing ──
	// Refuses to start if DEV_MODE is true on a production-looking host, so a
	// stray env var can't silently disable auth. Override with ALLOW_DEV_MODE_IN_PROD=true
	// (you'd never set that on a real deploy).
	devMode := os.Getenv("DEV_MODE") == "true"
	if devMode && os.Getenv("ALLOW_DEV_MODE_IN_PROD") != "true" {
		env := strings.ToLower(os.Getenv("ENV"))
		suffix := strings.ToLower(os.Getenv("DOMAIN_SUFFIX"))
		if env == "production" || env == "prod" || strings.HasSuffix(suffix, "eurobase.app") {
			log.Fatal("FATAL: DEV_MODE=true detected on a production-looking environment. " +
				"This disables all platform auth. Refuse to start. " +
				"Set ALLOW_DEV_MODE_IN_PROD=true to override (you shouldn't).")
		}
	}
	if devMode {
		slog.Warn("DEV MODE ACTIVE — platform auth is bypassed; never run this in production")
	}

	// ── Set up vault (encrypted secrets storage) ──
	// Required in production — OAuth secrets and vault entries need encryption.
	var vaultSvc *vault.VaultService
	if vaultKey := os.Getenv("VAULT_ENCRYPTION_KEY"); vaultKey != "" {
		var vaultErr error
		vaultSvc, vaultErr = vault.NewVaultService(pool, vaultKey)
		if vaultErr != nil {
			slog.Error("failed to initialize vault", "error", vaultErr)
			if !devMode {
				log.Fatalf("FATAL: vault initialization failed in production: %v", vaultErr)
			}
		} else {
			slog.Info("vault service initialized")
		}
	} else if !devMode {
		log.Fatal("FATAL: VAULT_ENCRYPTION_KEY is required in production — OAuth secrets cannot be stored without encryption")
	} else {
		slog.Warn("VAULT_ENCRYPTION_KEY not set, vault disabled (dev mode)")
	}

	// ── Migrate legacy plaintext OAuth secrets into the vault ──
	// Idempotent: once secrets have been moved, subsequent runs scan no rows.
	if vaultSvc != nil && vaultSvc.Configured() {
		if err := tenant.MigrateOAuthSecretsToVault(ctx, pool, vaultSvc); err != nil {
			slog.Error("oauth secret migration failed", "error", err)
		}
	}

	// ── Set up subdomain middleware for SDK URLs ({slug}.eurobase.app) ──
	domainSuffix := os.Getenv("DOMAIN_SUFFIX")
	if domainSuffix == "" {
		domainSuffix = "eurobase.app"
	}
	subdomainMw := auth.NewSubdomainMiddleware(pool, domainSuffix)

	// ── Edge Functions runner URL (internal ClusterIP of the Deno runner) ──
	fnRunnerURL := os.Getenv("FUNCTION_RUNNER_URL") // e.g. http://functions:8000
	if fnRunnerURL != "" {
		slog.Info("edge functions runner configured", "url", fnRunnerURL)
	} else {
		slog.Warn("FUNCTION_RUNNER_URL not set, edge function invocation will return 501")
	}

	// ── CORS allowlist ──
	// Always include wildcarded project subdomains + apex of the configured
	// domain suffix; callers can extend with ALLOWED_ORIGINS (comma-separated).
	allowedOrigins := []string{
		"https://*." + domainSuffix,
		"https://" + domainSuffix,
	}
	if extra := os.Getenv("ALLOWED_ORIGINS"); extra != "" {
		for _, o := range strings.Split(extra, ",") {
			if o = strings.TrimSpace(o); o != "" {
				allowedOrigins = append(allowedOrigins, o)
			}
		}
	}
	if devMode {
		allowedOrigins = append(allowedOrigins, "http://localhost:3000", "http://localhost:5173", "http://127.0.0.1:3000", "http://127.0.0.1:5173")
	}
	slog.Info("cors allowlist configured", "origins", allowedOrigins)

	// ── Set up chi router (extracted for testability) ──
	r := gateway.NewRouter(pool, developerPool, platformAuth, platformAuthSvc, limiter, s3Client, hub, logCh, subdomainMw, emailService, smsService, limitsSvc, vaultSvc, fnRunnerURL, allowedOrigins, devMode)

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
