package query

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestIntegration_PasswordHashNotExposed drives the real engine + /sql
// handler against a provisioned tenant schema (skips without a test DB,
// like the other engine tests) to lock in the wiring the unit tests
// can only check in isolation. Covers both surfaces the fix touches:
// the typed REST path (SelectRows) and the raw SDK SQL path
// (handleSQLInternal), for both a public-key caller and the exempt
// service key.
func TestIntegration_PasswordHashNotExposed(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	engine := NewQueryEngine(pool)

	svcCtx := ContextWithKeyType(context.Background(), "secret")
	pubCtx := ContextWithKeyType(context.Background(), "public")

	// Seed a user WITH a password_hash via the exempt service path
	// (a public-key insert of password_hash is itself rejected — see
	// below — so the service key is the way to plant the value).
	seeded, err := engine.InsertRow(svcCtx, schema, "users", map[string]interface{}{
		"email":         "victim@example.com",
		"password_hash": "$2a$10$examplehashdonotexpose",
	})
	if err != nil {
		t.Fatalf("seed insert (service): %v", err)
	}
	if _, ok := seeded["password_hash"]; !ok {
		t.Fatal("service-key insert should echo password_hash in RETURNING *")
	}

	byEmail := QueryParams{
		Filters: []Filter{{Column: "email", Operator: "eq", Value: "victim@example.com"}},
		Limit:   20,
	}

	// REST select=* (omitted Select) — public caller must NOT see the
	// hash, but must still get the ordinary columns.
	pubRows, _, err := engine.SelectRows(pubCtx, schema, "users", byEmail)
	if err != nil {
		t.Fatalf("public SelectRows(*): %v", err)
	}
	if len(pubRows) == 0 {
		t.Fatal("expected the seeded row back")
	}
	if _, leaked := pubRows[0]["password_hash"]; leaked {
		t.Error("select=* leaked password_hash to a public caller")
	}
	if pubRows[0]["email"] != "victim@example.com" {
		t.Errorf("non-sensitive column missing/wrong: %v", pubRows[0]["email"])
	}

	// Same query under the service key still returns the hash.
	svcRows, _, err := engine.SelectRows(svcCtx, schema, "users", byEmail)
	if err != nil {
		t.Fatalf("service SelectRows(*): %v", err)
	}
	if _, ok := svcRows[0]["password_hash"]; !ok {
		t.Error("service key should still receive password_hash")
	}

	// REST explicit select=password_hash — rejected for public caller.
	if _, _, err := engine.SelectRows(pubCtx, schema, "users",
		QueryParams{Select: []string{"id", "password_hash"}, Limit: 20}); err == nil {
		t.Error("explicit select=password_hash should be rejected for a public caller")
	}

	// REST insert of password_hash — rejected for public caller.
	if _, err := engine.InsertRow(pubCtx, schema, "users", map[string]interface{}{
		"email":         "attacker@example.com",
		"password_hash": "$2a$10$chosenhash",
	}); err == nil {
		t.Error("public-key insert of password_hash should be rejected")
	}

	// Raw SDK /sql path.
	sqlHandler := HandleSQL(engine) // forceReadOnly = true (SDK path)
	call := func(ctx context.Context, sql string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(SQLRequest{SQL: sql})
		req := httptest.NewRequest(http.MethodPost, "/v1/db/sql", bytes.NewReader(body))
		req = req.WithContext(ContextWithSchema(ctx, schema))
		rr := httptest.NewRecorder()
		sqlHandler(rr, req)
		return rr
	}

	// Public: explicit reference rejected before execution.
	if rr := call(pubCtx, "SELECT password_hash FROM users"); rr.Code != http.StatusBadRequest {
		t.Errorf("/sql explicit password_hash: want 400, got %d (%s)", rr.Code, rr.Body.String())
	}
	// Public: alias can't dodge it (input scan sees the real name).
	if rr := call(pubCtx, "SELECT password_hash AS ph FROM users"); rr.Code != http.StatusBadRequest {
		t.Errorf("/sql aliased password_hash: want 400, got %d (%s)", rr.Code, rr.Body.String())
	}
	// Public: SELECT * rejected by the output backstop.
	if rr := call(pubCtx, "SELECT * FROM users"); rr.Code != http.StatusBadRequest {
		t.Errorf("/sql SELECT *: want 400, got %d (%s)", rr.Code, rr.Body.String())
	}
	// Public: a safe projection still works.
	if rr := call(pubCtx, "SELECT id, email FROM users"); rr.Code != http.StatusOK {
		t.Errorf("/sql safe projection: want 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	// Service key: password_hash allowed through /sql.
	if rr := call(svcCtx, "SELECT password_hash FROM users"); rr.Code != http.StatusOK {
		t.Errorf("/sql service password_hash: want 200, got %d (%s)", rr.Code, rr.Body.String())
	}
}
