package query

import (
	"context"
	"testing"
)

func publicCtx() context.Context  { return ContextWithKeyType(context.Background(), "public") }
func serviceCtx() context.Context { return ContextWithKeyType(context.Background(), "secret") }

func TestCheckDeniedColumns_PublicRejectsSensitive(t *testing.T) {
	ctx := publicCtx()
	if err := checkDeniedColumns(ctx, "users", []string{"id", "email", "password_hash"}); err == nil {
		t.Fatal("expected password_hash on users to be rejected for public caller")
	}
	// Non-sensitive columns pass.
	if err := checkDeniedColumns(ctx, "users", []string{"id", "email", "display_name"}); err != nil {
		t.Fatalf("non-sensitive columns should pass: %v", err)
	}
	// "*" is not a named sensitive column — handled by stripping, not rejection.
	if err := checkDeniedColumns(ctx, "users", []string{"*"}); err != nil {
		t.Fatalf("select=* should not be rejected here: %v", err)
	}
	// Ordinary (non-system) table has no denylist.
	if err := checkDeniedColumns(ctx, "todos", []string{"anything", "password_hash"}); err != nil {
		t.Fatalf("non-system table should have no denied columns: %v", err)
	}
}

func TestCheckDeniedColumns_ServiceKeyExempt(t *testing.T) {
	ctx := serviceCtx()
	if err := checkDeniedColumns(ctx, "users", []string{"password_hash"}); err != nil {
		t.Fatalf("service key must be able to read password_hash (auth/admin server-side): %v", err)
	}
}

func TestCheckDeniedRelation_RejectsWildcardAndSensitive(t *testing.T) {
	ctx := publicCtx()
	if err := checkDeniedRelation(ctx, "users", []string{"*"}); err == nil {
		t.Fatal("users(*) embedding must be rejected for public caller (row_to_json can't be stripped)")
	}
	if err := checkDeniedRelation(ctx, "users", []string{"id", "password_hash"}); err == nil {
		t.Fatal("users(password_hash) embedding must be rejected")
	}
	// Naming only safe columns is allowed.
	if err := checkDeniedRelation(ctx, "users", []string{"id", "email"}); err != nil {
		t.Fatalf("users(id,email) embedding should be allowed: %v", err)
	}
	// Service key may embed anything.
	if err := checkDeniedRelation(serviceCtx(), "users", []string{"*"}); err != nil {
		t.Fatalf("service key embedding should be allowed: %v", err)
	}
}

func TestGuardSDKSQLInput_RejectsSensitiveRefs(t *testing.T) {
	ctx := publicCtx()
	reject := []string{
		"SELECT password_hash FROM users WHERE id = 'x'",
		"SELECT u.password_hash FROM users u",
		"SELECT password_hash AS ph FROM users",                    // alias bypass of an output-only check
		`SELECT "password_hash" FROM users`,                        // quoted identifier
		"SELECT ph FROM (SELECT password_hash AS ph FROM users) t", // subquery
		"select PASSWORD_HASH from users",                          // case-insensitive
	}
	for _, q := range reject {
		if err := guardSDKSQLInput(ctx, q); err == nil {
			t.Errorf("expected rejection for: %s", q)
		}
	}

	allow := []string{
		"SELECT id, email, display_name FROM users",
		"SELECT 'password_hash' AS label FROM users", // string literal, not an identifier
		"SELECT id FROM users -- password_hash in a comment",
		"SELECT count(*) FROM users",
	}
	for _, q := range allow {
		if err := guardSDKSQLInput(ctx, q); err != nil {
			t.Errorf("unexpected rejection for %q: %v", q, err)
		}
	}

	// Service key is exempt.
	if err := guardSDKSQLInput(serviceCtx(), "SELECT password_hash FROM users"); err != nil {
		t.Errorf("service key should be exempt: %v", err)
	}
}

func TestGuardSDKSQLOutput_RejectsSelectStar(t *testing.T) {
	// SELECT * returns password_hash under its real name.
	if err := guardSDKSQLOutput(publicCtx(), []string{"id", "email", "password_hash"}); err == nil {
		t.Fatal("expected rejection when password_hash is in returned columns")
	}
	if err := guardSDKSQLOutput(publicCtx(), []string{"id", "email"}); err != nil {
		t.Fatalf("safe columns should pass: %v", err)
	}
	if err := guardSDKSQLOutput(serviceCtx(), []string{"password_hash"}); err != nil {
		t.Fatalf("service key should be exempt: %v", err)
	}
}

func TestStripDeniedColumns_ScrubsForPublicKeepsForService(t *testing.T) {
	mkRows := func() []map[string]interface{} {
		return []map[string]interface{}{
			{"id": "u1", "email": "a@x.com", "password_hash": "$2a$secret"},
			{"id": "u2", "email": "b@x.com", "password_hash": "$2a$secret2"},
		}
	}

	// Public caller: password_hash scrubbed, other columns intact.
	rows := mkRows()
	stripDeniedColumns(publicCtx(), "users", rows)
	for i, row := range rows {
		if _, ok := row["password_hash"]; ok {
			t.Fatalf("row %d still exposes password_hash to public caller", i)
		}
		if row["email"] == nil {
			t.Fatalf("row %d lost a non-sensitive column", i)
		}
	}

	// Service caller: nothing scrubbed.
	rows = mkRows()
	stripDeniedColumns(serviceCtx(), "users", rows)
	if _, ok := rows[0]["password_hash"]; !ok {
		t.Fatal("service key must still receive password_hash")
	}

	// Other system tables' secrets are also covered.
	vault := []map[string]interface{}{{"name": "k", "secret": []byte("x"), "nonce": []byte("y")}}
	stripDeniedColumns(publicCtx(), "vault_secrets", vault)
	if _, ok := vault[0]["secret"]; ok {
		t.Fatal("vault_secrets.secret must be scrubbed for public caller")
	}
	if _, ok := vault[0]["nonce"]; ok {
		t.Fatal("vault_secrets.nonce must be scrubbed for public caller")
	}
}
