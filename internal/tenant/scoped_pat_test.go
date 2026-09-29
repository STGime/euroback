package tenant

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Project-scoped tokens end to end at the service level (#702): creation
// needs a project and a role not above the creator's; validation carries
// the scope; the role function caps it; the project list is filtered;
// a legacy token keeps working everywhere.
func TestScopedPATs(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()
	owner := insertTestPlatformUser(t, pool, "pat-owner@test.eurobase.local")
	viewer := insertTestPlatformUser(t, pool, "pat-viewer@test.eurobase.local")
	svc := &TenantService{pool: pool, developerPool: pool}
	mk := func(slug string) string {
		p, err := svc.CreateProject(ctx, owner, "pat-owner@test.eurobase.local", CreateProjectRequest{Name: slug, Slug: slug, Region: "fr-par", Plan: "free", OrgIDExplicit: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cleanupProject(t, pool, p.ID) })
		return p.ID
	}
	p1, p2 := mk("test-pat-one"), mk("test-pat-two")
	if _, err := pool.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'viewer')`, p1, viewer); err != nil {
		t.Fatal(err)
	}
	// As in production when CI provides the role URLs: the token service
	// on the gateway login, the role lookup on the developer login — a
	// grant change on personal_access_tokens / org tables fails here.
	patPool, devPool := pool, pool
	if u := os.Getenv("APIKEY_TEST_GATEWAY_URL"); u != "" {
		gp, err := pgxpool.New(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(gp.Close)
		patPool = gp
	}
	if u := os.Getenv("PAT_TEST_DEVELOPER_URL"); u != "" {
		dp, err := pgxpool.New(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(dp.Close)
		devPool = dp
	}
	pats := auth.NewPATService(patPool).WithProjectRole(func(ctx context.Context, c *auth.Claims, projectID string) (string, error) {
		return CallerProjectRole(ctx, devPool, patPool, c, projectID)
	})
	session := func(uid, email string) *auth.Claims {
		return &auth.Claims{Subject: uid, Email: email, LoginVia: auth.LoginViaPassword}
	}
	ownerSession := session(owner, "pat-owner@test.eurobase.local")

	// Creation rules.
	if _, err := pats.Create(ctx, auth.CreateInput{Claims: ownerSession, Name: "no project"}); err == nil {
		t.Fatal("token without a project created")
	}
	if _, err := pats.Create(ctx, auth.CreateInput{Claims: ownerSession, Name: "owner role", ProjectID: p1, Role: "owner"}); err == nil {
		t.Fatal("owner-role token created")
	}
	viewerSession := session(viewer, "pat-viewer@test.eurobase.local")
	if _, err := pats.Create(ctx, auth.CreateInput{Claims: viewerSession, Name: "too high", ProjectID: p1, Role: "developer"}); !errors.Is(err, auth.ErrPATRoleTooHigh) {
		t.Fatalf("viewer created a developer token: %v", err)
	}
	if _, err := pats.Create(ctx, auth.CreateInput{Claims: viewerSession, Name: "not mine", ProjectID: p2, Role: "viewer"}); !errors.Is(err, auth.ErrPATProjectNotFound) {
		t.Fatalf("token for a project without access: %v", err)
	}
	dev, err := pats.Create(ctx, auth.CreateInput{Claims: ownerSession, Name: "lovable", ProjectID: p1, Role: "developer"})
	if err != nil {
		t.Fatal(err)
	}
	if dev.PAT.ProjectID == nil || *dev.PAT.ProjectID != p1 || dev.PAT.Role == nil || *dev.PAT.Role != "developer" || dev.PAT.ExpiresAt != nil {
		t.Fatalf("created token = %+v", dev.PAT)
	}

	// Validation carries the scope; the role function caps it.
	c, err := pats.Validate(ctx, dev.PlaintextToken)
	if err != nil {
		t.Fatal(err)
	}
	if c.PATProjectID != p1 || c.PATRole != "developer" || c.PATID == "" || c.LoginVia != auth.LoginViaPAT {
		t.Fatalf("claims = %+v", c)
	}
	if role, _ := CallerProjectRole(ctx, pool, pool, c, p1); role != "developer" {
		t.Fatalf("own project role = %q, want developer (owner capped)", role)
	}
	if role, _ := CallerProjectRole(ctx, pool, pool, c, p2); role != "" {
		t.Fatalf("other project role = %q, want none", role)
	}
	list, err := svc.ListProjects(ctx, c)
	if err != nil || len(list) != 1 || list[0].ID != p1 {
		t.Fatalf("scoped list = %+v (%v)", list, err)
	}

	// Legacy token (no project): unchanged, all projects at the user's role.
	legacy := "eb_pat_legacy0123456789abcdef0123456789ab"
	if _, err := pool.Exec(ctx, `INSERT INTO personal_access_tokens (user_id, name, prefix, token_hash)
		VALUES ($1, 'legacy', 'eb_pat_legacy0', encode(sha256($2::bytea), 'hex'))`, owner, legacy); err != nil {
		t.Fatal(err)
	}
	lc, err := pats.Validate(ctx, legacy)
	if err != nil || lc.PATProjectID != "" {
		t.Fatalf("legacy claims = %+v (%v)", lc, err)
	}
	for _, p := range []string{p1, p2} {
		if role, _ := CallerProjectRole(ctx, pool, pool, lc, p); role != "owner" {
			t.Fatalf("legacy role on %s = %q, want owner", p, role)
		}
	}
	if list, _ := svc.ListProjects(ctx, lc); len(list) < 2 {
		t.Fatalf("legacy list = %d projects", len(list))
	}

	// Removing the creator from the project stops the token there.
	vt, err := pats.Create(ctx, auth.CreateInput{Claims: viewerSession, Name: "reader", ProjectID: p1, Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	vc, _ := pats.Validate(ctx, vt.PlaintextToken)
	if _, err := pool.Exec(ctx, `DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`, p1, viewer); err != nil {
		t.Fatal(err)
	}
	if role, _ := CallerProjectRole(ctx, pool, pool, vc, p1); role != "" {
		t.Fatalf("removed creator's token still works: %q", role)
	}

	// A scoped token uses the scoped prefix; a scoped row presented with
	// the legacy prefix (or the reverse) is refused — fail closed.
	if !strings.HasPrefix(dev.PlaintextToken, auth.ScopedPATPrefix) {
		t.Fatalf("scoped token prefix: %s", dev.PlaintextToken[:8])
	}
	swapped := "eb_ptk_legacy0123456789abcdef0123456789ab"
	if _, err := pool.Exec(ctx, `INSERT INTO personal_access_tokens (user_id, name, prefix, token_hash)
		VALUES ($1, 'swapped', 'eb_ptk_legacy', encode(sha256($2::bytea), 'hex'))`, owner, swapped); err != nil {
		t.Fatal(err)
	}
	if _, err := pats.Validate(ctx, swapped); err == nil {
		t.Fatal("legacy row accepted with the scoped prefix")
	}

	// Deleting the project deletes its tokens.
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM personal_access_tokens WHERE project_id = $1`, p1).Scan(&n)
	if n != 2 {
		t.Fatalf("tokens for p1 = %d, want 2", n)
	}
	cleanupProject(t, pool, p1)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM personal_access_tokens WHERE project_id = $1`, p1).Scan(&n)
	if n != 0 {
		t.Fatalf("tokens left after deleting the project: %d", n)
	}
	if _, err := pats.Validate(ctx, dev.PlaintextToken); err == nil {
		t.Fatal("token of a deleted project still validates")
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM personal_access_tokens WHERE user_id = ANY($1::uuid[])`, []string{owner, viewer}) })
}
