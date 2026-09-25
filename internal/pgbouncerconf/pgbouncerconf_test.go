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
		TenantPoolSize: 2, PlatformPoolSizes: map[string]int{"eurobase_gateway": 10}}
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
	if err := CheckTenantBudget(Settings{Replicas: 2, TenantPoolSize: 2}); err != nil {
		t.Errorf("2×2+2 within FuncConnLimit: %v", err)
	}
	if err := CheckTenantBudget(Settings{Replicas: 4, TenantPoolSize: 2}); err == nil {
		t.Error("4×2+2 > FuncConnLimit: want error")
	}
}
