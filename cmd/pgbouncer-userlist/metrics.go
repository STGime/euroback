package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/pgbouncerconf"
	"github.com/jackc/pgx/v5"
)

// serveMetrics exposes PgBouncer's pool state in Prometheus text format
// on PGB_METRICS_ADDR (#651). It reads the admin console (SHOW POOLS,
// SHOW LISTS) as the stats-only user over the pod-local listener; labels
// are per database alias only (no per-tenant cardinality).
func serveMetrics(ctx context.Context, c *config) {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprint(w, collectMetrics(r.Context(), c))
	})
	srv := &http.Server{Addr: c.metrics, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		srv.Close() //nolint:errcheck
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("pgbouncer metrics server", "error", err)
	}
}

func collectMetrics(ctx context.Context, c *config) string {
	var b strings.Builder
	b.WriteString("# HELP pgbouncer_up 1 if the admin console answered.\n# TYPE pgbouncer_up gauge\n")
	pools, lists, err := scrape(ctx, c)
	if err != nil {
		slog.Warn("pgbouncer metrics scrape failed", "error", err)
		b.WriteString("pgbouncer_up 0\n")
		return b.String()
	}
	b.WriteString("pgbouncer_up 1\n")

	type agg struct {
		clActive, clWaiting, svActive, svIdle, svUsed float64
		maxwait                                       float64
	}
	byDB := map[string]*agg{}
	for _, p := range pools {
		db := p["database"]
		if db == "pgbouncer" {
			continue
		}
		a := byDB[db]
		if a == nil {
			a = &agg{}
			byDB[db] = a
		}
		a.clActive += num(p["cl_active"])
		a.clWaiting += num(p["cl_waiting"])
		a.svActive += num(p["sv_active"])
		a.svIdle += num(p["sv_idle"])
		a.svUsed += num(p["sv_used"])
		if w := num(p["maxwait"]) + num(p["maxwait_us"])/1e6; w > a.maxwait {
			a.maxwait = w
		}
	}
	dbs := make([]string, 0, len(byDB))
	for db := range byDB {
		dbs = append(dbs, db)
	}
	sort.Strings(dbs)
	metric := func(name, help string, val func(*agg) float64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
		for _, db := range dbs {
			fmt.Fprintf(&b, "%s{database=%q} %g\n", name, db, val(byDB[db]))
		}
	}
	metric("pgbouncer_clients_active", "Clients linked to a server connection.", func(a *agg) float64 { return a.clActive })
	metric("pgbouncer_clients_waiting", "Clients waiting for a server connection.", func(a *agg) float64 { return a.clWaiting })
	metric("pgbouncer_servers_active", "Server connections linked to a client.", func(a *agg) float64 { return a.svActive })
	metric("pgbouncer_servers_idle", "Idle server connections.", func(a *agg) float64 { return a.svIdle + a.svUsed })
	metric("pgbouncer_maxwait_seconds", "Longest current client wait.", func(a *agg) float64 { return a.maxwait })

	// used_clients includes this scrape's own admin-console connection.
	clients := num(lists["used_clients"]) - 1
	if clients < 0 {
		clients = 0
	}
	// Published userlist age (#653): a stale list means new tenants can't
	// log in through the pooler (worker publisher down / misconfigured).
	if c.store != nil {
		if data, err := c.store.Get(ctx); err == nil {
			if t, err := time.Parse(time.RFC3339, string(data[pgbouncerconf.KeyPublishedAt])); err == nil {
				fmt.Fprintf(&b, "# HELP pgbouncer_userlist_published_age_seconds Seconds since the worker last published the tenant userlist.\n# TYPE pgbouncer_userlist_published_age_seconds gauge\npgbouncer_userlist_published_age_seconds %g\n", time.Since(t).Seconds())
			}
		}
	}
	fmt.Fprintf(&b, "# HELP pgbouncer_client_connections Client connections (used_clients, excluding the scrape).\n# TYPE pgbouncer_client_connections gauge\npgbouncer_client_connections %g\n", clients)
	fmt.Fprintf(&b, "# HELP pgbouncer_max_client_conn Configured max_client_conn.\n# TYPE pgbouncer_max_client_conn gauge\npgbouncer_max_client_conn %d\n", pgbouncerconf.MaxClientConn)
	return b.String()
}

func scrape(ctx context.Context, c *config) ([]map[string]string, map[string]string, error) {
	pw, err := statsPassword(c.dir)
	if err != nil {
		return nil, nil, err
	}
	cfg, err := pgx.ParseConfig(fmt.Sprintf("postgres://%s@127.0.0.1:6432/pgbouncer?sslmode=disable", statsUser))
	if err != nil {
		return nil, nil, err
	}
	cfg.Password = pw
	// The admin console speaks the simple query protocol only.
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(cctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close(context.Background()) //nolint:errcheck
	pools, err := rowsAsStrings(cctx, conn, "SHOW POOLS")
	if err != nil {
		return nil, nil, err
	}
	listRows, err := rowsAsStrings(cctx, conn, "SHOW LISTS")
	if err != nil {
		return nil, nil, err
	}
	lists := map[string]string{}
	for _, r := range listRows {
		lists[r["list"]] = r["items"]
	}
	return pools, lists, nil
}

func rowsAsStrings(ctx context.Context, conn *pgx.Conn, q string) ([]map[string]string, error) {
	rows, err := conn.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	fields := rows.FieldDescriptions()
	var out []map[string]string
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		m := map[string]string{}
		for i, f := range fields {
			m[f.Name] = fmt.Sprint(vals[i])
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func num(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
