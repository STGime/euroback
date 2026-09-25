// Command pgbouncer-userlist writes PgBouncer's pgbouncer.ini and auth
// file for the shared cluster and keeps the auth file current (#641).
//
//	pgbouncer-userlist init   write pgbouncer.ini + userlist.txt, exit
//	                          (init container, before PgBouncer starts)
//	pgbouncer-userlist sync   re-render the userlist every PGB_SYNC_INTERVAL
//	                          and SIGHUP PgBouncer when it changes (sidecar;
//	                          the pod shares its process namespace)
//
// Environment:
//
//	DATABASE_URL                  gateway role — upstream host/db, lists tenant schemas
//	DATABASE_URL_DEVELOPER        developer role, only with PGB_INCLUDE_DEVELOPER=1 (PR 4)
//	PGB_REPLICAS                  PgBouncer replicas (tenant connection budget check)
//	DATABASE_URL_FUNCTION_RUNNER  runner role (optional)
//	FUNC_PASSWORD_SECRET          derives tenant function-role verifiers
//	PGB_DIR                       output directory (default /run/pgbouncer)
//	PGB_SERVER_TLS                server_tls_sslmode (default require)
//	PGB_MAX_DB_CONNECTIONS        per-instance cap on server connections (default 20)
//	PGB_TENANT_POOL_SIZE          per-tenant pool (default 2)
//	PGB_SYNC_INTERVAL             userlist refresh (default 10s)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/eurobase/euroback/internal/pgbouncerconf"
	"github.com/eurobase/euroback/internal/tenantlogin"
	"github.com/jackc/pgx/v5"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	mode := "sync"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("pgbouncer-userlist: config", "error", err)
		os.Exit(1)
	}
	switch mode {
	case "init":
		if err := writeFile(filepath.Join(cfg.dir, "pgbouncer.ini"), pgbouncerconf.RenderINI(cfg.settings)); err != nil {
			fail("write pgbouncer.ini", err)
		}
		if _, err := syncUserlist(ctx, cfg); err != nil {
			fail("write userlist", err)
		}
		slog.Info("pgbouncer config written", "dir", cfg.dir)
	case "sync":
		runSync(ctx, cfg)
	default:
		fail("usage", fmt.Errorf("unknown mode %q (init|sync)", mode))
	}
}

func fail(what string, err error) {
	slog.Error("pgbouncer-userlist: "+what, "error", err)
	os.Exit(1)
}

type config struct {
	dir      string
	gateway  string
	platform []pgbouncerconf.PlatformUser
	secret   []byte
	settings pgbouncerconf.Settings
	interval time.Duration
}

func loadConfig() (*config, error) {
	c := &config{
		dir:      envOr("PGB_DIR", "/run/pgbouncer"),
		gateway:  os.Getenv("DATABASE_URL"),
		secret:   []byte(os.Getenv("FUNC_PASSWORD_SECRET")),
		interval: 10 * time.Second,
	}
	if c.gateway == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	if len(c.secret) < tenantlogin.MinSecretLen {
		return nil, fmt.Errorf("FUNC_PASSWORD_SECRET must be at least %d bytes", tenantlogin.MinSecretLen)
	}
	// DATABASE_URL_DEVELOPER (migrator-equivalent) is only added with an
	// explicit PGB_INCLUDE_DEVELOPER=1, set when the developer pool routes
	// through the pooler (PR 4) — so a future envFrom can't slip it in.
	keys := []string{"DATABASE_URL", "DATABASE_URL_FUNCTION_RUNNER"}
	if os.Getenv("PGB_INCLUDE_DEVELOPER") == "1" {
		keys = append(keys, "DATABASE_URL_DEVELOPER")
	}
	for _, key := range keys {
		v := os.Getenv(key)
		if v == "" {
			continue
		}
		p, err := pgbouncerconf.PlatformUserFromURL(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		c.platform = append(c.platform, p)
	}
	s := pgbouncerconf.Settings{
		ServerTLS:         envOr("PGB_SERVER_TLS", "require"),
		AuthFile:          filepath.Join(c.dir, "userlist.txt"),
		MaxDBConnections:  envInt("PGB_MAX_DB_CONNECTIONS", 20),
		TenantPoolSize:    envInt("PGB_TENANT_POOL_SIZE", 2),
		PlatformPoolSizes: map[string]int{},
	}
	if err := s.UpstreamFromURL(c.gateway); err != nil {
		return nil, err
	}
	for _, p := range c.platform {
		s.PlatformPoolSizes[p.User] = envInt("PGB_POOL_SIZE_"+strings.ToUpper(p.User), 10)
	}
	s.Replicas = envInt("PGB_REPLICAS", 1)
	if err := pgbouncerconf.CheckTenantBudget(s); err != nil {
		return nil, err
	}
	c.settings = s
	if d, err := time.ParseDuration(envOr("PGB_SYNC_INTERVAL", "10s")); err == nil && d > 0 {
		c.interval = d
	}
	return c, nil
}

// syncUserlist renders the userlist and writes it if it changed.
func syncUserlist(ctx context.Context, c *config) (bool, error) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := pgx.Connect(cctx, c.gateway)
	if err != nil {
		return false, fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(context.Background()) //nolint:errcheck
	schemas, err := pgbouncerconf.TenantSchemas(cctx, conn)
	if err != nil {
		return false, err
	}
	body, err := pgbouncerconf.RenderUserlist(c.platform, c.secret, schemas)
	if err != nil {
		return false, err
	}
	path := filepath.Join(c.dir, "userlist.txt")
	if old, err := os.ReadFile(path); err == nil && string(old) == body {
		return false, nil
	}
	return true, writeFile(path, body)
}

func runSync(ctx context.Context, c *config) {
	t := time.NewTicker(c.interval)
	defer t.Stop()
	// The file is written before the reload, so a failed SIGHUP must be
	// retried on the next pass even though the file then looks unchanged.
	pendingReload := false
	for {
		changed, err := syncUserlist(ctx, c)
		if err != nil {
			slog.Error("pgbouncer userlist sync failed", "error", err)
		}
		if changed {
			pendingReload = true
		}
		if pendingReload {
			if err := reloadPgBouncer(); err != nil {
				slog.Error("pgbouncer reload failed; retrying next pass", "error", err)
			} else {
				pendingReload = false
				slog.Info("pgbouncer userlist updated and reloaded")
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// reloadPgBouncer sends SIGHUP to the pgbouncer process in the shared
// process namespace; PgBouncer re-reads its config and auth file.
func reloadPgBouncer() error {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		comm, err := os.ReadFile(filepath.Join("/proc", e.Name(), "comm"))
		if err != nil || strings.TrimSpace(string(comm)) != "pgbouncer" {
			continue
		}
		return syscall.Kill(pid, syscall.SIGHUP)
	}
	return errors.New("pgbouncer process not found (shareProcessNamespace?)")
}

// writeFile writes atomically (temp file + rename), mode 0600.
func writeFile(path, body string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil && v > 0 {
		return v
	}
	return def
}
