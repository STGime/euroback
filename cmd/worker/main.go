// Package main is the entrypoint for the Eurobase River worker process.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"time"
	// Embed IANA tzdata so time.LoadLocation works in the alpine runtime
	// image, which ships no /usr/share/zoneinfo (cron schedule timezones).
	_ "time/tzdata"

	"github.com/eurobase/euroback/internal/cron"
	"github.com/eurobase/euroback/internal/db"
	"github.com/eurobase/euroback/internal/dbprovider"
	"github.com/eurobase/euroback/internal/email"
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

	// ── Set up outbound email + unsubscribe signer (Phase C) ──
	// Same TEM client + platform-JWT-derived unsubscribe secret as
	// the gateway uses. Missing TEM creds is a soft warning: the
	// drip worker's SendRaw will log-only, but the audit table
	// still lands the send-attempt row.
	consoleURL := strings.TrimRight(os.Getenv("CONSOLE_URL"), "/")
	if consoleURL == "" {
		consoleURL = "https://console.eurobase.app"
	}
	baseURL := strings.TrimRight(os.Getenv("GATEWAY_BASE_URL"), "/")
	if baseURL == "" {
		baseURL = "https://api.eurobase.app"
	}
	docsURL := consoleURL + "/docs"

	// Drip sender falls back to `hello@eurobase.app` if no explicit
	// env override — the persona split (`hello@` drip vs `noreply@`
	// transactional) is a locked-in decision in the public-beta
	// launch plan; hardcoding the fallback keeps a Secret typo from
	// silently pointing "reply to this email — we read replies" at a
	// dead noreply@ inbox.
	emailClient := email.NewEmailClient(
		os.Getenv("SCW_TEM_SECRET_KEY"),
		os.Getenv("SCW_TEM_REGION"),
		coalesceEnv("SCW_TEM_PROJECT_ID", "SCW_PROJECT_ID"),
		firstNonEmpty(os.Getenv("EMAIL_FROM_ADDRESS_DRIP"), "hello@eurobase.app"),
		firstNonEmpty(os.Getenv("EMAIL_FROM_NAME_DRIP"), "Eurobase"),
	)
	emailService := email.NewEmailService(emailClient, pool, consoleURL)
	if !emailClient.Configured() {
		slog.Warn("email client not configured — drip sends will log-only")
	}
	unsubSigner := email.NewUnsubscribeSigner(requireEnv("PLATFORM_JWT_SECRET"))

	// ── Team-tier database provider registry (M1) ──
	// Wire the Scaleway managed-PG provider if a secret is present.
	// Empty secret is legal — the provider returns ErrUnauthorized
	// on every method call, matching the pattern the vault + mollie
	// packages use for dev environments without production secrets.
	providerRegistry := dbprovider.NewRegistry()
	scwSecret := os.Getenv("SCW_SECRET_KEY")
	scwProject := os.Getenv("SCW_PROJECT_ID")
	scw := dbprovider.NewScaleway(dbprovider.ScalewayConfig{
		SecretKey:     scwSecret,
		ProjectID:     scwProject,
		DefaultRegion: firstNonEmpty(os.Getenv("SCW_RDB_REGION"), "fr-par"),
	})
	providerRegistry.Register(scw)
	if !scw.Configured() {
		slog.Warn("scaleway managed-PG provider not configured — Team-tier provisioning will fail with ErrUnauthorized until SCW_SECRET_KEY + SCW_PROJECT_ID are set")
	}

	// Password cipher for project_databases. Reuses the vault master
	// key so ops have one secret to rotate. Follows the same
	// dev-vs-prod pattern as cmd/gateway/main.go:303-317 — required
	// in production (any environment that will actually run Team-tier
	// provisioning), soft warning in dev/CI so the worker binary
	// still starts without secrets that pre-M1 dev environments
	// never needed.
	var cipher *dbprovider.Cipher
	if vaultKey := os.Getenv("VAULT_ENCRYPTION_KEY"); vaultKey != "" {
		var cErr error
		cipher, cErr = dbprovider.NewCipher(vaultKey, 1)
		if cErr != nil {
			slog.Error("failed to construct dbprovider cipher", "error", cErr)
			if isProdEnv() {
				os.Exit(1)
			}
		}
	} else if isProdEnv() {
		slog.Error("FATAL: VAULT_ENCRYPTION_KEY required in production for Team-tier provisioning")
		os.Exit(1)
	} else {
		slog.Warn("VAULT_ENCRYPTION_KEY not set — Team-tier provisioning will fail at seal time (dev mode)")
	}
	providerRepo := dbprovider.NewRepo(pool)

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
	river.AddWorker(riverWorkers, &workers.SendDripEmailWorker{
		DBPool:     pool,
		Emails:     emailService,
		Signer:     unsubSigner,
		BaseURL:    baseURL,
		ConsoleURL: consoleURL,
		DocsURL:    docsURL,
	})
	// Shared secret for deterministic eurobase_gateway password
	// derivation on the dedicated instance (M2.5 part 2b + backfill).
	// HMAC-SHA256(secret, project_database_id) ensures every runner
	// (provision retry, backfill sweeper, future rotate) sets the
	// same live password so Scaleway and the persisted ciphertext
	// can't diverge.
	//
	// Empty is legal (dev) — workers skip the bootstrap step and
	// leave the runtime slot NULL; instance stays usable via the
	// owner credential. A too-short secret (< 32 bytes) is a
	// configuration bug: HMAC-SHA256 keys shorter than the block
	// size are silently zero-padded, weakening the primitive.
	// Fail closed rather than silently derive from a weak key —
	// mirrors the analogous DDL_PASSWORD_SECRET requirement in
	// CLAUDE.md.
	const runtimePwSecretMinLen = 32
	runtimePwSecret := []byte(os.Getenv("RUNTIME_PASSWORD_SECRET"))
	switch {
	case len(runtimePwSecret) == 0:
		slog.Warn("RUNTIME_PASSWORD_SECRET not set — Team-tier bootstrap step will skip; runtime credential slot stays NULL")
	case len(runtimePwSecret) < runtimePwSecretMinLen:
		slog.Error("RUNTIME_PASSWORD_SECRET too short — must be at least 32 bytes",
			"len", len(runtimePwSecret), "min", runtimePwSecretMinLen)
		os.Exit(1)
	}

	river.AddWorker(riverWorkers, &workers.ProvisionTeamDatabaseWorker{
		Registry:              providerRegistry,
		Cipher:                cipher,
		Repo:                  providerRepo,
		RuntimePasswordSecret: runtimePwSecret,
	})
	river.AddWorker(riverWorkers, &workers.DeprovisionTeamDatabaseWorker{
		Registry: providerRegistry,
		Repo:     providerRepo,
	})
	// M3 restore worker — same cipher + registry + repo as
	// provisioning, plus the pool for state-machine writes on
	// restore_operations.
	river.AddWorker(riverWorkers, &workers.RestoreTeamDatabaseWorker{
		Pool:     pool,
		Registry: providerRegistry,
		Cipher:   cipher,
		Repo:     providerRepo,
	})

	// M2.5 part 2b backfill — runs BootstrapDedicated against
	// Team-tier projects provisioned before part 2b (runtime slot
	// empty). Fanned out one-per-project by BackfillSweeper below.
	river.AddWorker(riverWorkers, &workers.BackfillRuntimeCredentialWorker{
		Cipher:                cipher,
		Repo:                  providerRepo,
		RuntimePasswordSecret: runtimePwSecret,
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

	// ── Audit retention worker (#171) ──
	// Daily housekeeping: roll forward data_access_log monthly
	// partitions, drop ones past the retention horizon, optionally
	// prune audit_log row tails. Tuned via env vars; see
	// internal/workers/audit_retention.go.
	workers.StartAuditRetention(ctx, pool)

	// ── Backup sweeper (M3) ──
	// 6-hour ticker: refreshes backup_snapshots cache from
	// Provider.ListSnapshots for every Team project, prunes rows
	// past expires_at. Also cleans up superseded project_databases
	// rows past the 7-day rollback window via the existing
	// deprovision-eligible query.
	workers.StartBackupSweeper(ctx, pool, providerRegistry)

	// ── Backfill sweeper (M2.5 part 2b) ──
	// Hourly ticker: fans out BackfillRuntimeCredentialArgs jobs
	// (25/tick) for Team-tier projects still missing a non-owner
	// runtime credential. Idempotent — once the runtime slot is
	// populated, the WHERE filter excludes the row. Naturally
	// stops firing once the backlog drains.
	workers.StartBackfillSweeper(ctx, pool, riverClient)

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

// coalesceEnv returns the first non-empty value from the given env
// var keys. Used to fall back from a service-specific setting to a
// shared one (e.g. SCW_TEM_PROJECT_ID → SCW_PROJECT_ID).
func coalesceEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// firstNonEmpty returns the first non-empty string from the args.
// Handy for "prefer specific, fall back to shared" patterns where
// the values are already-resolved strings rather than env keys.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// isProdEnv reports whether the current environment looks like
// production — mirrors the shape used at cmd/gateway/main.go:288-290
// so the two binaries fail-closed on the same criteria. Any of:
//   - ENV=production or ENV=prod (case-insensitive)
//   - DOMAIN_SUFFIX ending in eurobase.app
func isProdEnv() bool {
	env := strings.ToLower(os.Getenv("ENV"))
	if env == "production" || env == "prod" {
		return true
	}
	return strings.HasSuffix(strings.ToLower(os.Getenv("DOMAIN_SUFFIX")), "eurobase.app")
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
