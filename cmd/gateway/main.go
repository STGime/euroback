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
	"strconv"
	"strings"
	"syscall"
	"time"
	// Embed IANA tzdata so time.LoadLocation works in the alpine runtime
	// image, which ships no /usr/share/zoneinfo (schedules API timezones).
	_ "time/tzdata"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/compliance"
	"github.com/eurobase/euroback/internal/db"
	"github.com/eurobase/euroback/internal/email"
	"github.com/eurobase/euroback/internal/enduser"
	"github.com/eurobase/euroback/internal/functions"
	"github.com/eurobase/euroback/internal/gateway"
	"github.com/eurobase/euroback/internal/metrics"
	"github.com/eurobase/euroback/internal/plans"
	"github.com/eurobase/euroback/internal/query"
	"github.com/eurobase/euroback/internal/ratelimit"
	"github.com/eurobase/euroback/internal/realtime"
	"github.com/eurobase/euroback/internal/sms"
	"github.com/eurobase/euroback/internal/storage"
	"github.com/eurobase/euroback/internal/tenant"
	"github.com/eurobase/euroback/internal/vault"
	"github.com/eurobase/euroback/internal/workers"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
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

	// Metrics server listens on a separate private port so it is never
	// exposed through the public ingress. Default 9100 is conventional for
	// node-level exporters and cleanly distinct from the API port.
	metricsPort := os.Getenv("METRICS_PORT")
	if metricsPort == "" {
		metricsPort = "9100"
	}
	buildVersion := os.Getenv("BUILD_VERSION")

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

	// Tenant migrations executor (#190). Runs each migration under a
	// per-tenant LOGIN role the gateway connects as: the developer pool
	// (as migrator) sets the role's derived password, then a short-lived
	// connection AS that role runs the SQL — so a malicious body can reach
	// exactly one tenant. Disabled (endpoint 503) when DDL_PASSWORD_SECRET
	// is unset; never falls back to a privileged pool. See migration 000063.
	migrationExec := query.NewMigrationExecutor(developerPool, databaseURL, []byte(os.Getenv("DDL_PASSWORD_SECRET")))
	if migrationExec.Enabled() {
		slog.Info("tenant migrations enabled")
	} else {
		slog.Warn("DDL_PASSWORD_SECRET not set — tenant migrations endpoint will return 503 until configured")
	}

	// ── Set up plan limits ──
	limitsSvc := plans.NewLimitsService(pool)
	slog.Info("plan limits service initialized")

	// ── Idle-pause worker ── (Phase B of the public-beta launch plan)
	// Hourly scan of Free projects that haven't seen a signed request
	// in 30 days; flips them to state='paused'. The wake-on-request
	// path in the subdomain middleware handles the reverse direction
	// with a deliberate ~30 s pause to make "Pro never pauses" a
	// visible pain point.
	go plans.NewIdlePauseWorker(pool).Run(ctx)
	slog.Info("idle-pause worker scheduled")

	// ── Set up platform auth ──
	platformAuthSvc := auth.NewPlatformAuthService(pool, platformJWTSecret)
	platformAuthSvc.AllowPublicSignup = os.Getenv("ALLOW_PUBLIC_SIGNUP") == "true"
	if !platformAuthSvc.AllowPublicSignup {
		slog.Info("signup gated behind platform_allowlist (set ALLOW_PUBLIC_SIGNUP=true to open)")
	}
	patSvc := auth.NewPATService(pool)
	platformAuth := auth.NewPlatformAuthMiddleware(platformAuthSvc).WithPATService(patSvc)

	// ── River insert-only client for onboarding-drip enqueue (Phase C) ──
	// The gateway only enqueues; the worker pod runs the actual jobs.
	// A River client with no Workers is "insert-only" and safe here.
	riverInsertOnly, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		slog.Error("failed to create river insert-only client", "error", err)
		os.Exit(1)
	}
	platformAuthSvc.SetDripEnqueuer(func(ctx context.Context, tx pgx.Tx, userID string, signupTime time.Time) error {
		return workers.EnqueueOnboardingSeries(ctx, riverInsertOnly, tx, userID, signupTime)
	})
	slog.Info("onboarding drip enqueuer wired")

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

	// ── Periodic sweeper for expired OAuth state rows (closes #58) ──
	// 10-minute cadence matches the state TTL so a row sits in the
	// table for at most ~20 minutes after expiry. consumeOAuthState
	// no longer cleans opportunistically on lookup miss, so this
	// goroutine is the sole reaper.
	go enduser.RunOAuthStateSweeper(ctx, pool, 10*time.Minute)

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

	// ── Functions runner HMAC signer ──
	// Closes layer 3 of advisory GHSA-7428-mvpp-rhr7. The runner verifies
	// the signature on every /invoke; cluster-internal forgery is blocked.
	// Production: secret missing aborts startup. Dev/staging without
	// FUNCTIONS_RUNNER_HMAC_SECRET runs unsigned (the runner is in soft
	// mode by default, so requests still succeed); a clear warning is
	// logged.
	var fnSigner *functions.Signer
	if secret := os.Getenv("FUNCTIONS_RUNNER_HMAC_SECRET"); secret != "" {
		s, err := functions.NewSigner(secret)
		if err != nil {
			log.Fatalf("FATAL: FUNCTIONS_RUNNER_HMAC_SECRET invalid: %v", err)
		}
		fnSigner = s
		slog.Info("functions runner HMAC signing enabled")
	} else {
		// Fail closed in obvious-prod environments. Same fence shape as
		// DEV_MODE (line ~219).
		env := strings.ToLower(os.Getenv("ENV"))
		suffix := strings.ToLower(os.Getenv("DOMAIN_SUFFIX"))
		if env == "production" || env == "prod" || strings.HasSuffix(suffix, "eurobase.app") {
			log.Fatal("FATAL: FUNCTIONS_RUNNER_HMAC_SECRET is required in production. " +
				"Generate via `openssl rand -hex 32` and add to the eurobase-secrets k8s Secret.")
		}
		slog.Warn("FUNCTIONS_RUNNER_HMAC_SECRET not set — gateway will send UNSIGNED requests to the functions runner. Only acceptable in dev/staging while the runner is in soft mode.")
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

	// ── Start DSAR export cleanup job (delete expired exports + S3 objects hourly) ──
	if s3Client != nil {
		exportCleanupSvc := compliance.NewExportService(pool, s3Client, nil)
		go func() {
			ticker := time.NewTicker(1 * time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					exportCleanupSvc.CleanupExpired(ctx)
				}
			}
		}()
		slog.Info("DSAR export cleanup job started (hourly)")
	}

	// ── Set up Prometheus metrics (served on a private port) ──
	metricsReg := metrics.New(buildVersion)
	go func() {
		if err := metricsReg.Serve(ctx, "0.0.0.0:"+metricsPort); err != nil {
			slog.Error("metrics server error", "error", err)
		}
	}()

	// ── Personal-data access logger (Tier-1 GDPR #4) ──
	// Async, sampled, batched writer to public.data_access_log. Reads on the
	// gateway pool; never on the request critical path. Config:
	//   AUDIT_DATA_ACCESS_ENABLED      "false" disables it entirely (default on)
	//   AUDIT_DATA_ACCESS_READ_SAMPLE  P(log) for reads, 0..1 (default 1 = all)
	accessCfg := audit.DefaultAccessRecorderConfig()
	if os.Getenv("AUDIT_DATA_ACCESS_ENABLED") == "false" {
		accessCfg.Enabled = false
	}
	if v := os.Getenv("AUDIT_DATA_ACCESS_READ_SAMPLE"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
			accessCfg.ReadSample = f
		} else {
			slog.Warn("ignoring invalid AUDIT_DATA_ACCESS_READ_SAMPLE", "value", v)
		}
	}
	accessRecorder := audit.NewAccessRecorder(pool, accessCfg)

	// ── Unsubscribe token signer (Phase C) ──
	// HMAC-signs opt-out URLs baked into every outbound platform
	// mail footer. Shares the PLATFORM_JWT_SECRET so gateway +
	// worker agree on the derived HMAC key.
	unsubSigner := email.NewUnsubscribeSigner(platformJWTSecret)

	// ── Set up chi router (extracted for testability) ──
	r := gateway.NewRouter(pool, developerPool, migrationExec, platformAuth, platformAuthSvc, limiter, accessRecorder, s3Client, hub, logCh, subdomainMw, emailService, smsService, limitsSvc, vaultSvc, fnRunnerURL, fnSigner, os.Getenv("FUNCTIONS_RUNNER_HMAC_SECRET"), metricsReg, allowedOrigins, unsubSigner, devMode)

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

	// Flush buffered data-access-log events before closing the pool.
	accessRecorder.Close(shutdownCtx)

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
