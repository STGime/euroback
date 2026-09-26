package workers

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/compliance"
	"github.com/eurobase/euroback/internal/dbprovider"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// #663: a Team-tier project's tenant data lives on its dedicated database;
// the shared cluster has no schema for it (post PR-A). The export must read
// the dedicated database as its owner, refuse a not-yet-active one, and
// never fall back to the shared cluster.
//
// Needs a superuser URL to a database migrated by scripts/db/apply-migrations.sh
// on a server with SSL enabled (the owner DSN uses sslmode=require); the
// test creates a second database on the same server as the "dedicated" one:
//
//	TEAM_EXPORT_TEST_ADMIN_URL=postgres://postgres@localhost:5432/eurobase \
//	go test ./internal/workers/ -run TeamTier
func TestTeamTierExport_ReadsDedicatedDatabase(t *testing.T) {
	adminURL := os.Getenv("TEAM_EXPORT_TEST_ADMIN_URL")
	if adminURL == "" {
		t.Skip("TEAM_EXPORT_TEST_ADMIN_URL not set")
	}
	ctx := context.Background()
	platform, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer platform.Close()

	suffix := randHex(t, 4)
	projectID := "7d0f1c2e-0000-4000-8000-" + randHex(t, 6)
	schema := "tenant_" + strings.ReplaceAll(projectID, "-", "_")
	userID := "11111111-2222-3333-4444-555555555555"
	dedDB, owner, ownerPW := "ded_"+suffix, "ded_owner_"+suffix, "pw_"+suffix

	// ── The dedicated instance: its own database, owner role, tenant schema.
	mustExec(t, platform, `CREATE ROLE `+owner+` LOGIN PASSWORD '`+ownerPW+`'`)
	mustExec(t, platform, `CREATE DATABASE `+dedDB+` OWNER `+owner)
	t.Cleanup(func() {
		_, _ = platform.Exec(context.Background(), `DROP DATABASE IF EXISTS `+dedDB+` WITH (FORCE)`)
		_, _ = platform.Exec(context.Background(), `DROP ROLE IF EXISTS `+owner)
	})
	u, _ := url.Parse(adminURL)
	u.Path = "/" + dedDB
	dedAdmin, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`SET ROLE ` + owner,
		`CREATE SCHEMA ` + schema,
		`CREATE TABLE ` + schema + `.users (id uuid PRIMARY KEY, email text)`,
		`ALTER TABLE ` + schema + `.users ENABLE ROW LEVEL SECURITY`,
		`CREATE POLICY none ON ` + schema + `.users USING (false)`,
		`INSERT INTO ` + schema + `.users VALUES ('` + userID + `', 'member@team.test')`,
		`CREATE TABLE ` + schema + `.bookings (id int, user_id uuid)`,
		`ALTER TABLE ` + schema + `.bookings ENABLE ROW LEVEL SECURITY`,
		`CREATE POLICY none ON ` + schema + `.bookings USING (false)`,
		`INSERT INTO ` + schema + `.bookings VALUES (1, '` + userID + `'), (2, gen_random_uuid()), (3, gen_random_uuid())`,
	} {
		if _, err := dedAdmin.Exec(ctx, q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	dedAdmin.Close(ctx)

	// ── The platform side: project row (no tenant schema on this database,
	// as for a Team project after PR-A) and its live project_databases row.
	mustExec(t, platform, `INSERT INTO platform_users (id, email) VALUES ($1, $2)`, projectID, "owner-"+suffix+"@team.test")
	mustExec(t, platform, `INSERT INTO projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		VALUES ($1, $1, 'team', $2, $3, $4, 'fr-par', 'team', 'active')`, projectID, "team-"+suffix, schema, "b-team-"+suffix)
	t.Cleanup(func() {
		_, _ = platform.Exec(context.Background(), `DELETE FROM project_databases WHERE project_id = $1`, projectID)
		_, _ = platform.Exec(context.Background(), `DELETE FROM projects WHERE id = $1`, projectID)
	})
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	cipher, err := dbprovider.NewCipher(base64.StdEncoding.EncodeToString(key), 1)
	if err != nil {
		t.Fatal(err)
	}
	ct, nonce, ver, err := cipher.Seal(ownerPW)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	if port == 0 {
		port = 5432
	}
	repo := dbprovider.NewRepo(platform)
	rec, err := repo.InsertProvisioning(ctx, projectID, &dbprovider.Instance{
		ProviderID: "test-" + suffix, Host: u.Hostname(), Port: port, DBName: dedDB,
		Username: owner, State: dbprovider.StateActive, Region: "fr-par",
	}, "scaleway", ct, nonce, ver)
	if err != nil {
		t.Fatal(err)
	}

	resolver := func(c *dbprovider.Cipher) DedicatedResolver {
		return func(ctx context.Context, pid string) (*pgxpool.Pool, error) {
			p, err := dbprovider.OpenOwnerPool(ctx, repo, c, pid)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, nil
			}
			return p, err
		}
	}
	shared := compliance.ExportSource{Pool: platform}

	t.Run("export reads every row from the dedicated database", func(t *testing.T) {
		src, closeSrc, err := resolveExportSource(ctx, shared, platform, resolver(cipher), projectID)
		if err != nil {
			t.Fatalf("resolveExportSource: %v", err)
		}
		defer closeSrc()
		var buf bytes.Buffer
		res, err := compliance.WriteTenantExport(ctx, platform, src, &buf, schema, projectID, "exp-team", "json")
		if err != nil {
			t.Fatalf("WriteTenantExport: %v", err)
		}
		entries := unzipEntries(t, buf.Bytes())
		if n := rowsIn(t, entries, "tables/bookings.json"); n != 3 {
			t.Errorf("bookings: %d rows, want 3 (owner-only RLS table on the dedicated database)", n)
		}
		if n := rowsIn(t, entries, "tables/users.json"); n != 1 {
			t.Errorf("users: %d rows, want 1", n)
		}
		if !res.Complete {
			t.Errorf("export incomplete: %q", res.Warnings)
		}
	})

	t.Run("per-user export reads the dedicated database", func(t *testing.T) {
		src, closeSrc, err := resolveExportSource(ctx, shared, platform, resolver(cipher), projectID)
		if err != nil {
			t.Fatal(err)
		}
		defer closeSrc()
		var buf bytes.Buffer
		if _, err := compliance.WriteUserExport(ctx, platform, src, &buf, schema, projectID, userID, "exp-team-user", "json"); err != nil {
			t.Fatalf("WriteUserExport: %v", err)
		}
		if n := rowsIn(t, unzipEntries(t, buf.Bytes()), "tables/bookings.json"); n != 1 {
			t.Errorf("user's bookings: %d, want 1", n)
		}
	})

	t.Run("the gateway's user check finds the user on the dedicated database", func(t *testing.T) {
		ded, err := dbprovider.OpenOwnerPool(ctx, repo, cipher, projectID)
		if err != nil {
			t.Fatal(err)
		}
		defer ded.Close()
		svc := compliance.NewExportService(platform, nil, nil).WithTenantPoolResolver(
			func(context.Context, string) *pgxpool.Pool { return ded })
		ok, err := svc.UserExistsInTenant(ctx, projectID, userID)
		if err != nil || !ok {
			t.Errorf("UserExistsInTenant = %v, %v; want true", ok, err)
		}
	})

	t.Run("reading the shared cluster for a Team project fails instead of exporting nothing", func(t *testing.T) {
		var buf bytes.Buffer
		_, err := compliance.WriteTenantExport(ctx, platform, shared, &buf, schema, projectID, "exp-team-wrong", "json")
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("export from the shared cluster: err = %v, want tenant schema not found", err)
		}
	})

	t.Run("no cipher: fails, never the shared cluster", func(t *testing.T) {
		src, _, err := resolveExportSource(ctx, shared, platform, resolver(nil), projectID)
		if err == nil || src.Pool != nil {
			t.Errorf("src = %+v, err = %v; want an error and no source", src, err)
		}
	})

	t.Run("dedicated database not active yet: fails with ErrDedicatedNotReady", func(t *testing.T) {
		if err := repo.UpdateState(ctx, rec.ID, dbprovider.StateProvisioning, u.Hostname(), port); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = repo.UpdateState(ctx, rec.ID, dbprovider.StateActive, u.Hostname(), port) }()
		_, _, err := resolveExportSource(ctx, shared, platform, resolver(cipher), projectID)
		if !errors.Is(err, dbprovider.ErrDedicatedNotReady) {
			t.Errorf("err = %v, want ErrDedicatedNotReady", err)
		}
	})

	t.Run("project without a dedicated database reads the shared cluster", func(t *testing.T) {
		src, closeSrc, err := resolveExportSource(ctx, shared, platform, resolver(cipher), "7d0f1c2e-0000-4000-8000-00000000f7ee")
		defer closeSrc()
		if err != nil || src.Pool != platform {
			t.Errorf("src = %+v, err = %v; want the shared source", src, err)
		}
	})
}

func mustExec(t *testing.T, p *pgxpool.Pool, q string, args ...any) {
	t.Helper()
	if _, err := p.Exec(context.Background(), q, args...); err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
}

func randHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func unzipEntries(t *testing.T, b []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		body, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = body
	}
	return out
}

func rowsIn(t *testing.T, entries map[string][]byte, name string) int {
	t.Helper()
	body, ok := entries[name]
	if !ok {
		t.Errorf("missing %s", name)
		return -1
	}
	var rows []any
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Errorf("%s: %v", name, err)
		return -1
	}
	return len(rows)
}
