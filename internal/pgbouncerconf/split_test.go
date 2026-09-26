package pgbouncerconf

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/tenantlogin"
	"github.com/jackc/pgx/v5"
)

// The two production poolers (#651), started by scripts/test-pgbouncer.sh
// in prod mode: the runner's pooler serves tenant roles only (no platform
// password in the process user code can reach) and lists tenants as the
// low-privilege runner role; the gateway's serves platform roles only.
func TestPoolerSplit(t *testing.T) {
	runnerPooler, gatewayPooler := os.Getenv("PGB_TEST_RUNNER_POOLER"), os.Getenv("PGB_TEST_GATEWAY_POOLER")
	runnerMetrics, gatewayMetrics := os.Getenv("PGB_TEST_RUNNER_METRICS"), os.Getenv("PGB_TEST_GATEWAY_METRICS")
	secret := os.Getenv("PGB_TEST_SECRET")
	if runnerPooler == "" || gatewayPooler == "" {
		t.Skip("run via scripts/test-pgbouncer.sh")
	}
	ctx := context.Background()
	connect := func(base, db, user, pw string) error {
		cfg, err := pgx.ParseConfig(base + "/" + db + "?sslmode=disable")
		if err != nil {
			return err
		}
		cfg.User, cfg.Password = user, pw
		c, err := pgx.ConnectConfig(ctx, cfg)
		if err != nil {
			return err
		}
		defer c.Close(ctx)
		var one int
		return c.QueryRow(ctx, "SELECT 1").Scan(&one)
	}

	// A tenant with a login (the e2e test provisioned one).
	gw, err := pgx.Connect(ctx, gatewayPooler+"/eurobase?sslmode=disable&user=eurobase_gateway&password=localdev")
	if err != nil {
		t.Fatalf("gateway via gateway pooler: %v", err)
	}
	var schema string
	err = gw.QueryRow(ctx, `SELECT n.nspname FROM pg_namespace n JOIN pg_roles r ON r.rolname = n.nspname || '_func' AND r.rolcanlogin
		WHERE n.nspname ~ '^tenant_[0-9a-f_]+$' ORDER BY 1 LIMIT 1`).Scan(&schema)
	gw.Close(ctx)
	if err != nil {
		t.Fatalf("find tenant: %v", err)
	}
	tenantUser, tenantPw := tenantlogin.FuncRole(schema), tenantlogin.FuncPassword([]byte(secret), schema)

	if err := connect(runnerPooler, "eurobase_tenant", tenantUser, tenantPw); err != nil {
		t.Errorf("tenant via runner pooler: %v", err)
	}
	if err := connect(runnerPooler, "eurobase", "eurobase_gateway", "localdev"); err == nil {
		t.Error("runner pooler accepted the gateway role — it must hold no platform passwords")
	}
	if err := connect(gatewayPooler, "eurobase_tenant", tenantUser, tenantPw); err == nil {
		t.Error("gateway pooler served a tenant role")
	}

	for name, url := range map[string]string{"runner": runnerMetrics, "gateway": gatewayMetrics} {
		resp, err := http.Get(url)
		if err != nil {
			t.Errorf("%s metrics: %v", name, err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		wants := []string{"pgbouncer_up 1", "pgbouncer_clients_waiting{database=", "pgbouncer_client_connections ", "pgbouncer_max_client_conn 500"}
		if name == "runner" {
			wants = append(wants, "pgbouncer_userlist_published_age_seconds ") // #653
		}
		for _, want := range wants {
			if !strings.Contains(string(body), want) {
				t.Errorf("%s metrics missing %q:\n%s", name, want, body)
			}
		}
	}
}
