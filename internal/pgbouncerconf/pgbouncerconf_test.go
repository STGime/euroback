package pgbouncerconf

import (
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/tenantlogin"
)

func TestRenderUserlist(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	out, err := RenderUserlist([]PlatformUser{
		{User: "eurobase_gateway", Password: `pa"ss`},
		{User: "eurobase_gateway", Password: "dup"}, // deduplicated
		{User: "eurobase_function_runner", Password: "r"},
	}, secret, []string{"tenant_abc"})
	if err != nil {
		t.Fatal(err)
	}
	v, _ := tenantlogin.ScramVerifier(secret, "tenant_abc")
	want := `"eurobase_gateway" "pa""ss"` + "\n" +
		`"eurobase_function_runner" "r"` + "\n" +
		`"tenant_abc_func" "` + v + `"` + "\n"
	if out != want {
		t.Errorf("userlist =\n%s\nwant\n%s", out, want)
	}
}

func TestRenderINI(t *testing.T) {
	s := Settings{ServerTLS: "require", AuthFile: "/run/pgbouncer/userlist.txt", MaxDBConnections: 20,
		TenantPoolSize: 2, PlatformPoolSizes: map[string]int{"eurobase_gateway": 10}, IncludeTenants: true,
		StatsUser: "pgb_stats"}
	if err := s.UpstreamFromURL("postgres://u:p@db.example:14319/eurobase?sslmode=require"); err != nil {
		t.Fatal(err)
	}
	ini := RenderINI(s)
	for _, want := range []string{
		"eurobase = host=db.example port=14319 dbname=eurobase max_db_connections=20",
		"eurobase_tenant = host=db.example port=14319 dbname=eurobase max_db_connections=20",
		"eurobase_gateway = pool_size=10",
		"pool_mode = transaction",
		"auth_type = scram-sha-256",
		"server_reset_query = DISCARD ALL",
		"server_reset_query_always = 1",
		"max_prepared_statements = 200",
		"server_tls_sslmode = require",
		"default_pool_size = 2",
		"stats_users = pgb_stats",
	} {
		if !strings.Contains(ini, want) {
			t.Errorf("pgbouncer.ini missing %q", want)
		}
	}
}

func TestPlatformUserFromURL(t *testing.T) {
	p, err := PlatformUserFromURL("postgres://eurobase_gateway:s%40cret@h:5432/db")
	if err != nil || p.User != "eurobase_gateway" || p.Password != "s@cret" {
		t.Errorf("got %+v, %v", p, err)
	}
	if _, err := PlatformUserFromURL("postgres://h:5432/db"); err == nil {
		t.Error("want error for URL without credentials")
	}
}

func TestCheckTenantBudget(t *testing.T) {
	// One pooler, no peer: 2×2 + 0 + 2 = 6 ≤ 16.
	if err := CheckTenantBudget(Settings{Replicas: 2, TenantPoolSize: 2}); err != nil {
		t.Errorf("2×2+2 within FuncConnLimit: %v", err)
	}
	// Both poolers accounted for: gateway 2×2 + runner peer 4 + 2 = 10 ≤ 16.
	if err := CheckTenantBudget(Settings{Replicas: 2, TenantPoolSize: 2, PeerTenantConnections: 4}); err != nil {
		t.Errorf("2×2 + 4 peer + 2 within FuncConnLimit: %v", err)
	}
	// Over the limit: 2×6 + 4 peer + 2 = 18 > 16.
	if err := CheckTenantBudget(Settings{Replicas: 2, TenantPoolSize: 6, PeerTenantConnections: 4}); err == nil {
		t.Error("2×6 + 4 peer + 2 > FuncConnLimit: want error")
	}
}

// Platform-only Settings (no IncludeTenants) render no tenant alias, so a
// tenant role can't open an extra unbudgeted pool.
func TestRenderINI_PlatformOnly(t *testing.T) {
	s := Settings{ServerTLS: "require", AuthFile: "/run/pgbouncer/userlist.txt", MaxDBConnections: 10,
		TenantPoolSize: 2, PlatformPoolSizes: map[string]int{"eurobase_gateway": 10}}
	if err := s.UpstreamFromURL("postgres://u:p@db.example:14319/eurobase"); err != nil {
		t.Fatal(err)
	}
	ini := RenderINI(s)
	if strings.Contains(ini, "eurobase_tenant") {
		t.Error("platform-only pooler must not render the tenant alias")
	}
	if strings.Contains(ini, "stats_users") {
		t.Error("stats_users rendered without a StatsUser")
	}
}

// RenderINI can serve platform roles AND tenant _func in one pooler (both
// aliases rendered). The shipped gateway pooler is tenant-only today
// (PGB_PLATFORM_URL_VARS=""); this covers the renderer for a FUTURE
// combined pooler.
func TestRenderINI_PlatformAndTenant(t *testing.T) {
	s := Settings{ServerTLS: "require", AuthFile: "/run/pgbouncer/userlist.txt", MaxDBConnections: 10,
		TenantMaxDBConnections: 15, TenantPoolSize: 2, IncludeTenants: true,
		PlatformPoolSizes: map[string]int{"eurobase_gateway": 10}}
	if err := s.UpstreamFromURL("postgres://u:p@db.example:14319/eurobase"); err != nil {
		t.Fatal(err)
	}
	ini := RenderINI(s)
	if !strings.Contains(ini, s.TenantDatabase()+" = host=") {
		t.Error("must render the tenant alias")
	}
	if !strings.Contains(ini, "eurobase = host=") {
		t.Error("must render the platform alias when platform roles are present")
	}
}

// The runner's pooler serves no platform role: no platform alias, so a
// tenant role can't open an extra, unbudgeted pool on it.
func TestRenderINI_RunnerPoolerNoPlatformAlias(t *testing.T) {
	s := Settings{ServerTLS: "require", AuthFile: "/a", MaxDBConnections: 1, TenantMaxDBConnections: 15,
		TenantPoolSize: 2, PlatformPoolSizes: map[string]int{}, IncludeTenants: true, StatsUser: "pgb_stats"}
	if err := s.UpstreamFromURL("postgres://u:p@db.example:14319/eurobase"); err != nil {
		t.Fatal(err)
	}
	ini := RenderINI(s)
	if strings.Contains(ini, "\neurobase = ") {
		t.Errorf("runner pooler rendered the platform alias:\n%s", ini)
	}
	for _, want := range []string{"eurobase_tenant = host=db.example", "max_client_conn = 500", "auth_file = /a"} {
		if !strings.Contains(ini, want) {
			t.Errorf("missing %q", want)
		}
	}
}
