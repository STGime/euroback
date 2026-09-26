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
//	PGB_UPSTREAM_URL_VAR          env var holding the URL for upstream host/db + tenant listing (default DATABASE_URL)
//	PGB_PLATFORM_URL_VARS         platform roles served, as env var names (default DATABASE_URL,DATABASE_URL_FUNCTION_RUNNER; "" for none)
//	PGB_INCLUDE_TENANTS           1 (default): tenant alias + tenant roles; 0: platform roles only
//	PGB_METRICS_ADDR              /metrics listen address (default :9127)
//	DATABASE_URL_DEVELOPER        developer role, only with PGB_INCLUDE_DEVELOPER=1 (PR 4)
//	PGB_REPLICAS                  PgBouncer replicas (tenant connection budget check)
//	DATABASE_URL_FUNCTION_RUNNER  runner role (optional)
//	FUNC_PASSWORD_SECRET          derives tenant function-role verifiers
//	PGB_DIR                       output directory (default /run/pgbouncer)
//	PGB_SERVER_TLS                server_tls_sslmode (default require)
//	PGB_MAX_DB_CONNECTIONS        per-instance server connections, platform alias (default 10)
//	PGB_TENANT_MAX_DB_CONNECTIONS per-instance server connections, tenant alias <db>_tenant (default 15)
//	PGB_QUERY_WAIT_TIMEOUT        seconds a client queues for a server connection (default 15)
//	PGB_TENANT_POOL_SIZE          per-tenant pool (default 2)
//	PGB_SYNC_INTERVAL             userlist refresh (default 10s)
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	upstream string // DB URL: upstream host/db and tenant listing (read-only catalog query)
	platform []pgbouncerconf.PlatformUser
	secret   []byte
	tenants  bool
	settings pgbouncerconf.Settings
	interval time.Duration
	metrics  string
}

// statsUser may run SHOW commands on PgBouncer's admin console; its
// password is random per pod (statsPasswordFile), used only by /metrics.
const statsUser = "pgb_stats"

func loadConfig() (*config, error) {
	c := &config{
		dir:      envOr("PGB_DIR", "/run/pgbouncer"),
		upstream: os.Getenv(envOr("PGB_UPSTREAM_URL_VAR", "DATABASE_URL")),
		secret:   []byte(os.Getenv("FUNC_PASSWORD_SECRET")),
		tenants:  envOr("PGB_INCLUDE_TENANTS", "1") == "1",
		interval: 10 * time.Second,
		metrics:  envOr("PGB_METRICS_ADDR", ":9127"),
	}
	if c.upstream == "" {
		return nil, errors.New("upstream database URL is required (PGB_UPSTREAM_URL_VAR, default DATABASE_URL)")
	}
	if c.tenants && len(c.secret) < tenantlogin.MinSecretLen {
		return nil, fmt.Errorf("FUNC_PASSWORD_SECRET must be at least %d bytes", tenantlogin.MinSecretLen)
	}
	// Platform roles this pooler serves, by env var name. The runner's
	// pooler serves none (#651: the process user code can reach holds no
	// platform password); the gateway's serves the gateway role.
	// DATABASE_URL_DEVELOPER (migrator-equivalent) additionally needs an
	// explicit PGB_INCLUDE_DEVELOPER=1 (PR 4), so an envFrom can't slip it in.
	platformVars, set := os.LookupEnv("PGB_PLATFORM_URL_VARS")
	if !set {
		platformVars = "DATABASE_URL,DATABASE_URL_FUNCTION_RUNNER"
	}
	var keys []string
	for _, k := range strings.Split(platformVars, ",") {
		if k = strings.TrimSpace(k); k != "" && k != "DATABASE_URL_DEVELOPER" {
			keys = append(keys, k)
		}
	}
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
		ServerTLS:              envOr("PGB_SERVER_TLS", "require"),
		AuthFile:               filepath.Join(c.dir, "userlist.txt"),
		MaxDBConnections:       envInt("PGB_MAX_DB_CONNECTIONS", 10),
		TenantMaxDBConnections: envInt("PGB_TENANT_MAX_DB_CONNECTIONS", 15),
		QueryWaitTimeout:       envInt("PGB_QUERY_WAIT_TIMEOUT", 15),
		TenantPoolSize:         envInt("PGB_TENANT_POOL_SIZE", 2),
		PlatformPoolSizes:      map[string]int{},
		IncludeTenants:         c.tenants,
		StatsUser:              statsUser,
	}
	if err := s.UpstreamFromURL(c.upstream); err != nil {
		return nil, err
	}
	for _, p := range c.platform {
		s.PlatformPoolSizes[p.User] = envInt("PGB_POOL_SIZE_"+strings.ToUpper(p.User), 10)
	}
	s.Replicas = envInt("PGB_REPLICAS", 1)
	if c.tenants {
		if err := pgbouncerconf.CheckTenantBudget(s); err != nil {
			return nil, err
		}
	}
	c.settings = s
	if d, err := time.ParseDuration(envOr("PGB_SYNC_INTERVAL", "10s")); err == nil && d > 0 {
		c.interval = d
	}
	return c, nil
}

// syncUserlist renders the userlist and writes it if it changed.
func syncUserlist(ctx context.Context, c *config) (bool, error) {
	var schemas []string
	if c.tenants {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		conn, err := pgx.Connect(cctx, c.upstream)
		if err != nil {
			return false, fmt.Errorf("connect: %w", err)
		}
		defer conn.Close(context.Background()) //nolint:errcheck
		if schemas, err = pgbouncerconf.TenantSchemas(cctx, conn); err != nil {
			return false, err
		}
	}
	statsPw, err := statsPassword(c.dir)
	if err != nil {
		return false, err
	}
	platform := append(append([]pgbouncerconf.PlatformUser{}, c.platform...),
		pgbouncerconf.PlatformUser{User: statsUser, Password: statsPw})
	body, err := pgbouncerconf.RenderUserlist(platform, c.secret, schemas)
	if err != nil {
		return false, err
	}
	path := filepath.Join(c.dir, "userlist.txt")
	if old, err := os.ReadFile(path); err == nil && string(old) == body {
		return false, nil
	}
	return true, writeFile(path, body)
}

// statsPassword returns the pod's random stats-user password, creating it
// on first use (in the in-memory emptyDir, 0600).
func statsPassword(dir string) (string, error) {
	path := filepath.Join(dir, "stats_password")
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		return string(b), nil
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	pw := hex.EncodeToString(buf)
	return pw, writeFile(path, pw)
}

func runSync(ctx context.Context, c *config) {
	go serveMetrics(ctx, c)
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
