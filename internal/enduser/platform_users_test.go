package enduser

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/query"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// #807: the console users API as a server-side import / admin API —
// passwordless create, POST /users/batch, GET /users?email= (exact).
// Needs a migrated PG16, like TestPasskeyStore_DB: ENDUSER_PASSKEY_TEST_URL.
func TestPlatformUsersImport_DB(t *testing.T) {
	url := os.Getenv("ENDUSER_PASSKEY_TEST_URL")
	if url == "" {
		t.Skip("ENDUSER_PASSKEY_TEST_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("connect: %v", err)
	}
	t.Cleanup(pool.Close) // first, so it runs after the cleanup below

	pid := uuid.NewString()
	ownerEmail := "import-" + pid + "@t.local"
	if _, err := pool.Exec(ctx, `INSERT INTO platform_users (email) VALUES ($1)`, ownerEmail); err != nil {
		t.Fatal(err)
	}
	var ownerID, schema string
	_ = pool.QueryRow(ctx, `SELECT id FROM platform_users WHERE email=$1`, ownerEmail).Scan(&ownerID)
	if _, err := pool.Exec(ctx, `INSERT INTO projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		VALUES ($1,$2,'Import',$3,'tenant_import',$4,'fr-par','free','provisioning')`, pid, ownerID, "import-"+pid[:8], "import-"+pid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT provision_tenant($1,'Import','free')`, pid); err != nil {
		t.Fatal(err)
	}
	_ = pool.QueryRow(ctx, `SELECT schema_name FROM projects WHERE id=$1`, pid).Scan(&schema)
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `SELECT deprovision_tenant($1)`, pid)
		_, _ = pool.Exec(c, `DELETE FROM projects WHERE id=$1`, pid)
		_, _ = pool.Exec(c, `DELETE FROM platform_users WHERE id=$1`, ownerID)
	})

	call := func(h http.HandlerFunc, method, target, body string) (int, []byte) {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r = r.WithContext(query.ContextWithSchema(r.Context(), schema))
		w := httptest.NewRecorder()
		h(w, r)
		return w.Code, w.Body.Bytes()
	}

	// Passwordless create; email_confirmed defaults to true, can be false.
	code, body := call(handleCreateUser(pool, nil), http.MethodPost, "/", `{"email":"Ann@Example.com","display_name":"Ann"}`)
	if code != http.StatusCreated {
		t.Fatalf("passwordless create: %d %s", code, body)
	}
	var ann PlatformUser
	_ = json.Unmarshal(body, &ann)
	if ann.Email == nil || *ann.Email != "ann@example.com" || ann.EmailConfirmedAt == nil || ann.DisplayName == nil || *ann.DisplayName != "Ann" {
		t.Fatalf("passwordless user = %+v", ann)
	}
	var hasHash bool
	_ = pool.QueryRow(ctx, `SELECT password_hash IS NOT NULL FROM `+quoteIdent(schema)+`.users WHERE id=$1`, ann.ID).Scan(&hasHash)
	if hasHash {
		t.Fatal("passwordless user got a password hash")
	}
	if code, body := call(handleCreateUser(pool, nil), http.MethodPost, "/", `{"email":"unconfirmed@example.com","email_confirmed":false}`); code != http.StatusCreated || !bytes.Contains(body, []byte(`"email_confirmed_at":null`)) {
		t.Fatalf("create unconfirmed: %d %s", code, body)
	}
	long := strings.Repeat("x", 73)
	if code, body := call(handleCreateUser(pool, nil), http.MethodPost, "/", `{"email":"long@example.com","password":"`+long+`"}`); code != http.StatusBadRequest || !bytes.Contains(body, []byte("72")) {
		t.Fatalf("73-byte password must be a 400, not a 500: %d %s", code, body)
	}

	// Exact email lookup vs substring search.
	if _, err := pool.Exec(ctx, `INSERT INTO `+quoteIdent(schema)+`.users (email) VALUES ('joann@example.com')`); err != nil {
		t.Fatal(err)
	}
	list := func(q string) PlatformUserList {
		code, body := call(handleListUsers(pool, nil), http.MethodGet, "/?"+q, "")
		if code != http.StatusOK {
			t.Fatalf("list %s: %d %s", q, code, body)
		}
		var l PlatformUserList
		_ = json.Unmarshal(body, &l)
		return l
	}
	if l := list("email=ANN@example.com"); l.Total != 1 || len(l.Users) != 1 || l.Users[0].ID != ann.ID {
		t.Fatalf("email= exact: %+v", l)
	}
	if l := list("search=ann@example.com"); l.Total != 2 {
		t.Fatalf("search= is a substring match (ann + joann): %+v", l)
	}
	if l := list("email=nobody@example.com"); l.Total != 0 || len(l.Users) != 0 {
		t.Fatalf("email= no match: %+v", l)
	}

	// Batch import.
	hash, _ := bcrypt.GenerateFromPassword([]byte("supabase-pw-1"), 4)
	keepID := uuid.NewString()
	batch := map[string]any{"users": []map[string]any{
		{"email": "member1@example.com"}, // 0 created, passwordless
		{"email": "Member2@Example.com", "password_hash": string(hash), "id": keepID, "metadata": map[string]any{"role": "admin"}}, // 1 created, keeps id + hash
		{"email": "ann@example.com"},                                    // 2 exists
		{"email": "not-an-email"},                                       // 3 invalid
		{"email": "member3@example.com", "password": "plaintext-pw"},    // 4 invalid: plain password
		{"email": "member4@example.com", "password_hash": "not-bcrypt"}, // 5 invalid
		{"email": "member1@example.com"},                                // 6 invalid: duplicate in request
		{"email": "member5@example.com", "email_confirmed": false},      // 7 created, unconfirmed
	}}
	b, _ := json.Marshal(batch)
	code, body = call(handleBatchCreateUsers(pool, nil), http.MethodPost, "/batch", string(b))
	if code != http.StatusOK {
		t.Fatalf("batch: %d %s", code, body)
	}
	var br batchResponse
	_ = json.Unmarshal(body, &br)
	want := []string{"created", "created", "exists", "invalid", "invalid", "invalid", "invalid", "created"}
	if br.Created != 3 || br.Exists != 1 || br.Invalid != 4 || len(br.Results) != len(want) {
		t.Fatalf("batch counts: %+v", br)
	}
	for i, st := range want {
		if br.Results[i].Index != i || br.Results[i].Status != st {
			t.Fatalf("results[%d] = %+v, want %s", i, br.Results[i], st)
		}
	}
	if br.Results[1].ID != keepID {
		t.Fatalf("given id not kept: %+v", br.Results[1])
	}
	var gotHash string
	var confirmed bool
	_ = pool.QueryRow(ctx, `SELECT password_hash, email_confirmed_at IS NOT NULL FROM `+quoteIdent(schema)+`.users WHERE id=$1`, keepID).Scan(&gotHash, &confirmed)
	if bcrypt.CompareHashAndPassword([]byte(gotHash), []byte("supabase-pw-1")) != nil || !confirmed {
		t.Fatalf("imported hash / confirmation: %q %v", gotHash, confirmed)
	}
	var unconfirmed bool
	_ = pool.QueryRow(ctx, `SELECT email_confirmed_at IS NULL FROM `+quoteIdent(schema)+`.users WHERE email='member5@example.com'`).Scan(&unconfirmed)
	if !unconfirmed {
		t.Fatal("email_confirmed:false not applied in batch")
	}

	// Same batch again: everything valid now exists, nothing changes.
	code, body = call(handleBatchCreateUsers(pool, nil), http.MethodPost, "/batch", string(b))
	_ = json.Unmarshal(body, &br)
	if code != http.StatusOK || br.Created != 0 || br.Exists != 4 {
		t.Fatalf("re-run must be idempotent: %d %+v", code, br)
	}

	// Limits.
	if code, _ := call(handleBatchCreateUsers(pool, nil), http.MethodPost, "/batch", `{"users":[]}`); code != http.StatusBadRequest {
		t.Fatalf("empty batch: %d", code)
	}
	tooMany := make([]map[string]any, maxBatchUsers+1)
	for i := range tooMany {
		tooMany[i] = map[string]any{"email": "x@example.com"}
	}
	b, _ = json.Marshal(map[string]any{"users": tooMany})
	if code, _ := call(handleBatchCreateUsers(pool, nil), http.MethodPost, "/batch", string(b)); code != http.StatusBadRequest {
		t.Fatalf("batch over the cap: %d", code)
	}
}
