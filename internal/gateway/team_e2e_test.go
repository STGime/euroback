package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/cron"
	"github.com/eurobase/euroback/internal/dbprovider"
	"github.com/eurobase/euroback/internal/functions"
	"github.com/eurobase/euroback/internal/plans"
	"github.com/eurobase/euroback/internal/storage"
	"github.com/eurobase/euroback/internal/tenant"
	"github.com/eurobase/euroback/internal/tenantlogin"
	"github.com/eurobase/euroback/internal/vault"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestTeamEndToEnd (#683) drives the real gateway router for a Team project
// whose tenant data lives on its own "dedicated" Postgres, and checks every
// feature reaches that database — and never the shared cluster.
//
// Two scenarios, each on its own dedicated database:
//   - fresh:    a project created as Team — the shared cluster has no
//     tenant schema, so a misrouted request errors;
//   - upgraded: a Pro project upgraded to Team — the shared cluster still
//     holds a stale copy of the schema with a decoy row, so a misrouted
//     request *succeeds* against the wrong database (the realistic prod
//     failure). Assertions read the dedicated DB directly and the decoy
//     schema must stay exactly as it was.
//
// Run via scripts/test-team.sh. Known routing gaps are listed in
// teamKnownGaps with the error they fail with today: such a check SKIPs
// only while it fails *that way*; any other failure is a FAIL, and a pass
// is a FAIL too ("gap fixed — remove it from teamKnownGaps"), so the list
// can't go stale or mask a new bug. Requests that never reach the handler
// (401/403, membership 404, maintenance 503) always FAIL.
func TestTeamEndToEnd(t *testing.T) {
	cfg := teamTestConfig{
		sharedAdmin:  os.Getenv("TEAM_TEST_SHARED_ADMIN"),
		sharedGW:     os.Getenv("TEAM_TEST_SHARED_GATEWAY"),
		sharedDev:    os.Getenv("TEAM_TEST_SHARED_DEVELOPER"),
		sharedRunner: os.Getenv("TEAM_TEST_SHARED_RUNNER"),
		deno:         os.Getenv("TEAM_TEST_DENO"),
		dedOwner:     os.Getenv("TEAM_TEST_DED_OWNER"),
		dedAdmin:     os.Getenv("TEAM_TEST_DED_ADMIN"),
		s3Endpoint:   os.Getenv("TEAM_TEST_S3_ENDPOINT"),
		s3Key:        os.Getenv("TEAM_TEST_S3_KEY"),
		s3Secret:     os.Getenv("TEAM_TEST_S3_SECRET"),
	}
	if cfg.sharedAdmin == "" || cfg.dedOwner == "" || cfg.s3Endpoint == "" {
		t.Skip("run via scripts/test-team.sh")
	}
	// One instance-wide secret, as in prod: the runtime roles are
	// cluster-wide, so both scenarios' bootstraps must set the same password.
	cfg.runtimePW, cfg.readonlyPW = randHexT(t, 16), randHexT(t, 16)

	for _, sc := range []struct {
		name     string
		db       string
		upgraded bool
	}{
		{"fresh", "eb_fresh", false},
		{"upgraded", "eb_upgraded", true},
		// Free / Pro: the same checks on the shared cluster (no dedicated
		// database) — every change for Team must keep these working.
		{"free", "eurobase", false},
	} {
		t.Run(sc.name, func(t *testing.T) {
			runTeamChecks(t, setupTeamProject(t, cfg, sc.name, sc.db, sc.upgraded))
		})
	}
}

func runTeamChecks(t *testing.T, env *teamEnv) {
	sdk := func(method, path, body string) *httpResult {
		t.Helper()
		req := httptest.NewRequest(method, "http://"+env.slug+".eurobase.test"+path, strings.NewReader(body))
		req.Header.Set("apikey", env.secretKey)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		env.router.ServeHTTP(rec, req)
		return &httpResult{req: method + " " + path, code: rec.Code, body: rec.Body.String()}
	}
	console := func(method, path, body string) *httpResult {
		t.Helper()
		req := httptest.NewRequest(method, "http://api.eurobase.test/platform/projects/"+env.projectID+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+env.platformJWT)
		rec := httptest.NewRecorder()
		env.router.ServeHTTP(rec, req)
		return &httpResult{req: "console " + method + " " + path, code: rec.Code, body: rec.Body.String()}
	}

	// Credential cipher step 2: the worker re-seals legacy (raw master
	// key) Team credentials with the domain-separated version.
	check(t, env, "credential_reseal", func() error {
		ctx := context.Background()
		versions := func() (string, error) {
			var v string
			err := env.shared.QueryRow(ctx, `SELECT password_key_version || '/' || runtime_password_key_version || '/' || readonly_password_key_version
				FROM project_databases WHERE project_id = $1 AND deleted_at IS NULL`, env.projectID).Scan(&v)
			return v, err
		}
		if v, err := versions(); err != nil || v != "1/1/1" {
			return fmt.Errorf("before: key versions %q (%v), want 1/1/1", v, err)
		}
		current, err := dbprovider.NewCipher(env.keyB64, dbprovider.CipherVersion)
		if err != nil {
			return err
		}
		// As the worker runs it: on its pool (eurobase_gateway).
		repo := dbprovider.NewRepo(env.gw)
		// Rows of the other scenarios (own keys, placeholder credentials)
		// fail to open and are skipped — reported, never a panic or an abort.
		if _, _, err := repo.ResealLegacy(ctx, current); err != nil {
			return fmt.Errorf("ResealLegacy: %w", err)
		}
		want := fmt.Sprintf("%d/%d/%d", dbprovider.CipherVersion, dbprovider.CipherVersion, dbprovider.CipherVersion)
		if v, err := versions(); err != nil || v != want {
			return fmt.Errorf("after: key versions %q (%v), want %s", v, err, want)
		}
		// Idempotent, and the re-sealed owner credential still opens.
		if _, _, err := repo.ResealLegacy(ctx, current); err != nil {
			return fmt.Errorf("second pass: %w", err)
		}
		if v, err := versions(); err != nil || v != want {
			return fmt.Errorf("after a second pass: key versions %q (%v), want %s", v, err, want)
		}
		p, err := dbprovider.OpenOwnerPool(ctx, repo, current, env.projectID)
		if err != nil {
			return fmt.Errorf("open owner pool with the re-sealed credential: %w", err)
		}
		defer p.Close()
		return p.Ping(ctx)
	})

	check(t, env, "sdk_rest_insert_and_select", func() error {
		if r := sdk("POST", "/v1/db/todos", `{"title":"from-sdk"}`); r.code >= 300 {
			return r
		}
		if n := env.dedCount(t, "todos", "title = 'from-sdk'"); n != 1 {
			return fmt.Errorf("row not on the dedicated DB (count %d)", n)
		}
		r := sdk("GET", "/v1/db/todos", "")
		if r.code != 200 || !strings.Contains(r.body, "from-sdk") || strings.Contains(r.body, decoyTitle) {
			return fmt.Errorf("select read the wrong rows: %w", r)
		}
		return nil
	})

	// Proves the SDK runs on the dedicated instance as the non-owner
	// runtime role — an owner connection would bypass RLS.
	check(t, env, "sdk_sql_runtime_role_on_dedicated", func() error {
		r := sdk("POST", "/v1/db/sql", `{"sql":"SELECT current_user AS who, current_database() AS db, (SELECT count(*) FROM todos WHERE title = 'from-sdk') AS n"}`)
		if r.code != 200 {
			return r
		}
		for _, want := range []string{`"who":"eurobase_gateway"`, `"db":"` + env.dedDB + `"`, `"n":1`} {
			if !strings.Contains(r.body, want) {
				return fmt.Errorf("missing %s: %w", want, r)
			}
		}
		return nil
	})

	check(t, env, "sdk_enduser_signup_and_signin", func() error {
		creds := `{"email":"enduser@team.test","password":"Correct-horse-9"}`
		if r := sdk("POST", "/v1/auth/signup", creds); r.code >= 300 {
			return r
		}
		if n := env.dedCount(t, "users", "email = 'enduser@team.test'"); n != 1 {
			return fmt.Errorf("user not on the dedicated DB (count %d)", n)
		}
		if r := sdk("POST", "/v1/auth/signin", creds); r.code != 200 || !strings.Contains(r.body, "access_token") {
			return r
		}
		return nil
	})

	// SDK storage as a signed-in end user with the PUBLIC key, as an app
	// does: RLS applies (the secret key would be service role), so this
	// also proves the runtime — not owner — pool: another end user must
	// not see the file.
	check(t, env, "sdk_storage_upload_list_delete", func() error {
		signin := func(email string) (string, error) {
			if r := sdk("POST", "/v1/auth/signup", `{"email":"`+email+`","password":"Correct-horse-9"}`); r.code >= 300 && !strings.Contains(r.body, "already registered") {
				return "", r
			}
			r := sdk("POST", "/v1/auth/signin", `{"email":"`+email+`","password":"Correct-horse-9"}`)
			var tok struct {
				AccessToken string `json:"access_token"`
			}
			if r.code != 200 || json.Unmarshal([]byte(r.body), &tok) != nil || tok.AccessToken == "" {
				return "", fmt.Errorf("signin %s: %w", email, r)
			}
			return tok.AccessToken, nil
		}
		owner, err := signin("enduser@team.test")
		if err != nil {
			return err
		}
		other, err := signin("other@team.test")
		if err != nil {
			return err
		}
		as := func(token, method, path string) *httpResult {
			req := httptest.NewRequest(method, "http://api.eurobase.test"+path, nil)
			req.Header.Set("apikey", env.publicKey)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			env.router.ServeHTTP(rec, req)
			return &httpResult{req: method + " " + path, code: rec.Code, body: rec.Body.String()}
		}
		asUser := func(method, path string) *httpResult { return as(owner, method, path) }
		if r := env.upload(env.publicKey, owner, "http://api.eurobase.test/v1/storage/upload", "e2e/sdk.txt"); r.code >= 300 {
			return r
		}
		if r := as(other, "GET", "/v1/storage/e2e/sdk.txt"); r.code != http.StatusNotFound {
			return fmt.Errorf("another end user must not see the file (RLS; runtime pool): %w", r)
		}
		// Another end user can't list it, overwrite it, or get an upload
		// URL for it; the row keeps its owner.
		if r := as(other, "GET", "/v1/storage/"); r.code != 200 || strings.Contains(r.body, "e2e/sdk.txt") {
			return fmt.Errorf("another end user's listing must not include the file: %w", r)
		}
		if r := env.upload(env.publicKey, other, "http://api.eurobase.test/v1/storage/upload", "e2e/sdk.txt"); r.code != http.StatusForbidden {
			return fmt.Errorf("another end user's upload to the key must be refused: %w", r)
		}
		sreq := httptest.NewRequest("POST", "http://api.eurobase.test/v1/storage/signed-url", strings.NewReader(`{"key":"e2e/sdk.txt","operation":"upload","content_type":"text/plain"}`))
		sreq.Header.Set("apikey", env.publicKey)
		sreq.Header.Set("Authorization", "Bearer "+other)
		sreq.Header.Set("Content-Type", "application/json")
		srec := httptest.NewRecorder()
		env.router.ServeHTTP(srec, sreq)
		if srec.Code != http.StatusForbidden {
			return fmt.Errorf("another end user's signed upload URL for the key must be refused: %d %s", srec.Code, srec.Body.String())
		}
		if n := env.dedScalar(t, "SELECT count(*)::text FROM %s s JOIN "+pgx.Identifier{env.schema, "users"}.Sanitize()+
			" u ON u.id = s.uploaded_by WHERE s.key = 'e2e/sdk.txt' AND u.email = 'enduser@team.test'", "storage_objects"); n != "1" {
			return fmt.Errorf("the file changed owner (count %s)", n)
		}
		if r := asUser("GET", "/v1/storage/e2e/sdk.txt"); r.code >= 400 {
			return fmt.Errorf("owner download: %w", r)
		}
		if r := env.upload(env.publicKey, owner, "http://api.eurobase.test/v1/storage/upload", "e2e/sdk.txt"); r.code >= 300 {
			return fmt.Errorf("owner re-upload of their own key: %w", r)
		}
		// A console (service-role) overwrite is allowed but keeps the owner.
		if r := env.upload("", "", "http://api.eurobase.test/platform/projects/"+env.projectID+"/storage/upload", "e2e/sdk.txt"); r.code >= 300 {
			return fmt.Errorf("console overwrite: %w", r)
		}
		if n := env.dedScalar(t, "SELECT count(*)::text FROM %s s JOIN "+pgx.Identifier{env.schema, "users"}.Sanitize()+
			" u ON u.id = s.uploaded_by WHERE s.key = 'e2e/sdk.txt' AND u.email = 'enduser@team.test'", "storage_objects"); n != "1" {
			return fmt.Errorf("a console overwrite changed the owner (count %s)", n)
		}
		// Signed-URL upload: the key is claimed when the URL is issued, so
		// the file is tracked and listed for its uploader only.
		signed := func(token, key string) *httpResult {
			req := httptest.NewRequest("POST", "http://api.eurobase.test/v1/storage/signed-url",
				strings.NewReader(`{"key":"`+key+`","operation":"upload","content_type":"text/plain"}`))
			req.Header.Set("apikey", env.publicKey)
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			env.router.ServeHTTP(rec, req)
			return &httpResult{req: "POST signed-url " + key, code: rec.Code, body: rec.Body.String()}
		}
		sr := signed(owner, "e2e/signed.txt")
		var su struct {
			URL string `json:"url"`
		}
		if sr.code != 200 || json.Unmarshal([]byte(sr.body), &su) != nil || su.URL == "" {
			return fmt.Errorf("signed upload URL: %w", sr)
		}
		put, _ := http.NewRequest("PUT", su.URL, strings.NewReader("signed"))
		put.Header.Set("Content-Type", "text/plain")
		resp, err := http.DefaultClient.Do(put)
		if err != nil {
			return fmt.Errorf("PUT to signed URL: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("PUT to signed URL: %s", resp.Status)
		}
		if r := asUser("GET", "/v1/storage/"); !strings.Contains(r.body, "e2e/signed.txt") {
			return fmt.Errorf("the uploader's listing must include their signed-URL upload: %w", r)
		}
		if r := as(other, "GET", "/v1/storage/"); strings.Contains(r.body, "e2e/signed.txt") {
			return fmt.Errorf("another end user's listing must not include it: %w", r)
		}
		// An object in S3 without a tracking row has no owner to check:
		// end users can't claim it.
		if err := env.s3.UploadObject(context.Background(), env.bucket, "e2e/untracked.txt", strings.NewReader("x"), "text/plain", 1); err != nil {
			return err
		}
		if r := env.upload(env.publicKey, other, "http://api.eurobase.test/v1/storage/upload", "e2e/untracked.txt"); r.code != http.StatusForbidden {
			return fmt.Errorf("claiming an untracked existing object must be refused: %w", r)
		}
		// Filtered listing refills from later S3 pages: with limit=1 the
		// other user's only file sorts after three keys they can't see.
		if r := env.upload(env.publicKey, other, "http://api.eurobase.test/v1/storage/upload", "e2e/zz-other.txt"); r.code >= 300 {
			return r
		}
		if r := as(other, "GET", "/v1/storage/?limit=1"); r.code != 200 || !strings.Contains(r.body, "e2e/zz-other.txt") {
			return fmt.Errorf("limit=1 listing must reach the caller's file past others' keys: %w", r)
		}
		// A still-valid JWT of a deleted end user: 401, not 500.
		gone, err := signin("gone@team.test")
		if err != nil {
			return err
		}
		if _, err := env.ded.Exec(context.Background(), fmt.Sprintf(`DELETE FROM %s WHERE email = 'gone@team.test'`,
			pgx.Identifier{env.schema, "users"}.Sanitize())); err != nil {
			return err
		}
		if r := env.upload(env.publicKey, gone, "http://api.eurobase.test/v1/storage/upload", "e2e/gone.txt"); r.code != http.StatusUnauthorized {
			return fmt.Errorf("upload by a deleted end user: want 401: %w", r)
		}
		if n := env.dedCount(t, "storage_objects", "key = 'e2e/sdk.txt'"); n != 1 {
			return fmt.Errorf("storage metadata not on the dedicated DB (count %d)", n)
		}
		if n := env.dedScalar(t, "SELECT count(*)::text FROM %s s JOIN "+pgx.Identifier{env.schema, "users"}.Sanitize()+
			" u ON u.id = s.uploaded_by WHERE s.key = 'e2e/sdk.txt'", "storage_objects"); n != "1" {
			return fmt.Errorf("uploaded_by doesn't point at the dedicated users table (count %s)", n)
		}
		if r := asUser("GET", "/v1/storage/"); r.code != 200 || !strings.Contains(r.body, "e2e/sdk.txt") {
			return fmt.Errorf("list: %w", r)
		}
		if r := asUser("DELETE", "/v1/storage/e2e/sdk.txt"); r.code >= 300 {
			return r
		}
		if n := env.dedCount(t, "storage_objects", "key = 'e2e/sdk.txt'"); n != 0 {
			return fmt.Errorf("storage metadata not deleted on the dedicated DB (count %d)", n)
		}
		return nil
	})

	// #697 shared folders: developer-uploaded files become readable by end
	// users (never writable) once their folder is shared.
	check(t, env, "storage_shared_folders", func() error {
		ctx := context.Background()
		_ = ctx
		call := func(method, path, apiKey, bearer, body string) *httpResult {
			req := httptest.NewRequest(method, "http://api.eurobase.test"+path, strings.NewReader(body))
			if apiKey != "" {
				req.Header.Set("apikey", apiKey)
			}
			if bearer != "" {
				req.Header.Set("Authorization", "Bearer "+bearer)
			}
			if body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			env.router.ServeHTTP(rec, req)
			return &httpResult{req: method + " " + path, code: rec.Code, body: rec.Body.String()}
		}
		sharing := "/platform/projects/" + env.projectID + "/storage-sharing"
		setShare := func(prefix, vis string) *httpResult {
			return call("PUT", sharing+"/", "", env.platformJWT, `{"prefix":"`+prefix+`","visibility":"`+vis+`"}`)
		}
		r := call("POST", "/v1/auth/signin", env.publicKey, "", `{"email":"enduser@team.test","password":"Correct-horse-9"}`)
		var tok struct {
			AccessToken string `json:"access_token"`
		}
		if r.code != 200 || json.Unmarshal([]byte(r.body), &tok) != nil || tok.AccessToken == "" {
			return fmt.Errorf("signin: %w", r)
		}
		user := tok.AccessToken
		// Developer upload (console → no owner).
		if r := env.upload("", "", "http://api.eurobase.test/platform/projects/"+env.projectID+"/storage/upload", "themes/hero.txt"); r.code >= 300 {
			return fmt.Errorf("console upload: %w", r)
		}
		if r := call("GET", "/v1/storage/themes/hero.txt", env.publicKey, user, ""); r.code != http.StatusNotFound {
			return fmt.Errorf("unshared developer file must be invisible to end users: %w", r)
		}
		if r := setShare("exports/", "public"); r.code != http.StatusBadRequest {
			return fmt.Errorf("sharing exports/ must be refused: %w", r)
		}
		// End users can't create rules through the SDK table API.
		call("POST", "/v1/db/storage_shared_prefixes", env.publicKey, user, `{"prefix":"themes/","visibility":"public"}`)
		if n := env.dedCount(t, "storage_shared_prefixes", "true"); n != 0 {
			return fmt.Errorf("an end user created a sharing rule via the SDK (count %d)", n)
		}
		// authenticated
		if r := setShare("themes", "authenticated"); r.code != 200 || !strings.Contains(r.body, `"themes/"`) {
			return fmt.Errorf("set authenticated: %w", r)
		}
		if r := call("GET", "/v1/storage/themes/hero.txt", env.publicKey, user, ""); r.code != 200 || r.body != "hello team" {
			return fmt.Errorf("signed-in user must read an authenticated-shared file: %w", r)
		}
		if r := call("GET", "/v1/storage/?prefix=themes/", env.publicKey, user, ""); r.code != 200 || !strings.Contains(r.body, "themes/hero.txt") {
			return fmt.Errorf("signed-in user's listing must include it: %w", r)
		}
		if r := call("GET", "/v1/storage/themes/hero.txt", env.publicKey, "", ""); r.code != http.StatusNotFound {
			return fmt.Errorf("anonymous caller must not read an authenticated-shared file: %w", r)
		}
		// Never writable by end users.
		if r := env.upload(env.publicKey, user, "http://api.eurobase.test/v1/storage/upload", "themes/hero.txt"); r.code != http.StatusForbidden {
			return fmt.Errorf("end user overwrite of a shared file must be refused: %w", r)
		}
		if r := call("DELETE", "/v1/storage/themes/hero.txt", env.publicKey, user, ""); r.code < 400 {
			return fmt.Errorf("end user delete of a shared file must be refused: %w", r)
		}
		// The policies themselves, as the end user on the runtime role
		// (not superuser — RLS applies): no insert under a shared folder,
		// no moving an own row into one.
		uid := env.dedScalar(t, "SELECT id::text FROM %s WHERE email = 'enduser@team.test'", "users")
		// Refused means refused by RLS (42501), not any error.
		rlsRefused := func(err error) bool {
			var pgErr *pgconn.PgError
			return errors.As(err, &pgErr) && pgErr.Code == "42501"
		}
		asEndUser := func(stmts ...string) error {
			tx, err := env.rt.Begin(ctx)
			if err != nil {
				return err
			}
			defer tx.Rollback(ctx) //nolint:errcheck
			if _, err := tx.Exec(ctx, `SELECT set_config('app.end_user_id', $1, true), set_config('app.end_user_role', 'authenticated', true)`, uid); err != nil {
				return err
			}
			for _, q := range stmts {
				if _, err := tx.Exec(ctx, q, uid); err != nil {
					return err
				}
			}
			return nil
		}
		so := pgx.Identifier{env.schema, "storage_objects"}.Sanitize()
		if err := asEndUser(`INSERT INTO ` + so + ` (key, uploaded_by) VALUES ('themes/rls-plant.txt', $1::uuid)`); !rlsRefused(err) {
			return fmt.Errorf("RLS must refuse an end user's row under a shared folder: %v", err)
		}
		if err := asEndUser(`INSERT INTO `+so+` (key, uploaded_by) VALUES ('mine/rls.txt', $1::uuid)`,
			`UPDATE `+so+` SET key = 'themes/rls-moved.txt' WHERE key = 'mine/rls.txt' AND uploaded_by = $1::uuid`); !rlsRefused(err) {
			return fmt.Errorf("RLS must refuse moving an own row into a shared folder: %v", err)
		}
		if err := asEndUser(`INSERT INTO ` + so + ` (key, uploaded_by) VALUES ('mine/rls-ok.txt', $1::uuid)`); err != nil {
			return fmt.Errorf("RLS: an end user's own insert outside shared folders must work: %v", err)
		}
		// The data API is read-only on platform-managed tables for SDK callers.
		if r := call("PATCH", "/v1/db/storage_objects/00000000-0000-0000-0000-000000000000", env.publicKey, user, `{"size_bytes":0}`); r.code < 400 || !strings.Contains(r.body, "read-only") {
			return fmt.Errorf("data API writes to storage_objects must be refused: %w", r)
		}
		// …while the secret key (the developer's server) may still write it.
		heroID := env.dedScalar(t, "SELECT id::text FROM %s WHERE key = 'themes/hero.txt'", "storage_objects")
		if r := call("PATCH", "/v1/db/storage_objects/"+heroID, env.secretKey, "", `{"content_type":"text/plain"}`); r.code != 200 {
			return fmt.Errorf("secret-key write to storage_objects must still work: %w", r)
		}
		if r := call("GET", "/v1/db/storage_objects?select=uploaded_by", env.publicKey, user, ""); r.code < 400 {
			return fmt.Errorf("selecting uploaded_by via the data API must be refused: %w", r)
		}
		// Shared folders hold developer files only: an end user can't plant
		// a new file there (it would be served to everyone) — upload or
		// upload URL — nor squat a name before the developer uploads it.
		if r := env.upload(env.publicKey, user, "http://api.eurobase.test/v1/storage/upload", "themes/planted.html"); r.code != http.StatusForbidden {
			return fmt.Errorf("end user upload of a new file into a shared folder must be refused: %w", r)
		}
		if r := call("POST", "/v1/storage/signed-url", env.publicKey, user, `{"key":"themes/planted2.html","operation":"upload","content_type":"text/html"}`); r.code != http.StatusForbidden {
			return fmt.Errorf("end user upload URL into a shared folder must be refused: %w", r)
		}
		// …nor through the table API (the storage_insert policy refuses it).
		call("POST", "/v1/db/storage_objects", env.publicKey, user, `{"key":"themes/planted3.html","content_type":"text/html"}`)
		if n := env.dedCount(t, "storage_objects", "key LIKE 'themes/planted%'"); n != 0 {
			return fmt.Errorf("a planted file was recorded (count %d)", n)
		}
		// Who uploaded what isn't readable through the SDK table API.
		if r := call("GET", "/v1/db/storage_objects", env.publicKey, user, ""); strings.Contains(r.body, "uploaded_by") && r.code == 200 {
			return fmt.Errorf("uploaded_by must not be exposed via /v1/db: %w", r)
		}
		// public
		if r := setShare("themes/", "public"); r.code != 200 {
			return fmt.Errorf("set public: %w", r)
		}
		// Anonymous listing shows only public files, and the cursor never
		// reveals a hidden key (an S3 continuation token would): a private
		// file that sorts right after the public one must not leak.
		if r := env.upload(env.publicKey, user, "http://api.eurobase.test/v1/storage/upload", "themesz-private/a.txt"); r.code >= 300 {
			return fmt.Errorf("private upload: %w", r)
		}
		anonList := call("GET", "/v1/storage/?limit=1", env.publicKey, "", "")
		var page struct {
			Objects []struct {
				Key string `json:"key"`
			} `json:"objects"`
			NextCursor string `json:"next_cursor"`
		}
		if anonList.code != 200 || json.Unmarshal([]byte(anonList.body), &page) != nil ||
			len(page.Objects) != 1 || page.Objects[0].Key != "themes/hero.txt" {
			return fmt.Errorf("anonymous listing must show only public files: %w", anonList)
		}
		if page.NextCursor != "" && page.NextCursor != "themes/hero.txt" {
			return fmt.Errorf("anonymous listing cursor %q must be empty or the last visible key", page.NextCursor)
		}
		if r := call("GET", "/v1/storage/?cursor="+url.QueryEscape(page.NextCursor), env.publicKey, "", ""); r.code != 200 || strings.Contains(r.body, "themesz-private") {
			return fmt.Errorf("anonymous listing leaked a private key: %w", r)
		}
		// Serving headers (#697 follow-up): a file stored without a type is
		// served with a passive one from its extension, cacheable for
		// anonymous public reads; HEAD works; an uploaded HTML file is never
		// served as a document on the project's origin.
		raw := func(method, path string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(method, "http://api.eurobase.test"+path, nil)
			rec := httptest.NewRecorder()
			env.router.ServeHTTP(rec, req)
			return rec
		}
		pub := "?apikey=" + env.publicKey
		if rec := raw("GET", "/v1/storage/themes/hero.txt"+pub); rec.Code != 200 ||
			!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") ||
			rec.Header().Get("Cache-Control") != "public, max-age=300" {
			return fmt.Errorf("public GET headers: %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"))
		}
		if rec := raw("HEAD", "/v1/storage/themes/hero.txt"+pub); rec.Code != 200 || rec.Body.Len() != 0 ||
			rec.Header().Get("Content-Length") == "" {
			return fmt.Errorf("public HEAD: %d len=%q body=%d", rec.Code, rec.Header().Get("Content-Length"), rec.Body.Len())
		}
		// An HTML file — stored without a type, or explicitly as text/html
		// or a type list a browser would read as HTML — is never rendered
		// inline.
		for _, tc := range []struct{ key, partType string }{
			{"themes/page.html", ""},
			{"themes/explicit.html", "text/html"},
			{"themes/sneaky.txt", "text/plain, text/html"},
		} {
			var buf bytes.Buffer
			mw := multipart.NewWriter(&buf)
			_ = mw.WriteField("key", tc.key)
			hdr := textproto.MIMEHeader{}
			hdr.Set("Content-Disposition", `form-data; name="file"; filename="x"`)
			if tc.partType != "" {
				hdr.Set("Content-Type", tc.partType)
			}
			fw, _ := mw.CreatePart(hdr)
			_, _ = fw.Write([]byte("<script>alert(1)</script>"))
			_ = mw.Close()
			req := httptest.NewRequest("POST", "http://api.eurobase.test/platform/projects/"+env.projectID+"/storage/upload", &buf)
			req.Header.Set("Content-Type", mw.FormDataContentType())
			req.Header.Set("Authorization", "Bearer "+env.platformJWT)
			up := httptest.NewRecorder()
			env.router.ServeHTTP(up, req)
			if up.Code >= 300 {
				return fmt.Errorf("console upload %s: %d %s", tc.key, up.Code, up.Body.String())
			}
			rec := raw("GET", "/v1/storage/"+tc.key+pub)
			if rec.Code != 200 || rec.Header().Get("Content-Disposition") != "attachment" {
				return fmt.Errorf("%s (%q) must be served as an attachment: %d %q %q", tc.key, tc.partType, rec.Code,
					rec.Header().Get("Content-Type"), rec.Header().Get("Content-Disposition"))
			}
		}
		// Byte ranges (Safari / iOS media), validators, cache keying.
		rreq := httptest.NewRequest("GET", "http://api.eurobase.test/v1/storage/themes/hero.txt"+pub, nil)
		rreq.Header.Set("Range", "bytes=0-4")
		rrec := httptest.NewRecorder()
		env.router.ServeHTTP(rrec, rreq)
		if rrec.Code != http.StatusPartialContent || rrec.Body.String() != "hello" ||
			!strings.HasPrefix(rrec.Header().Get("Content-Range"), "bytes 0-4/") {
			return fmt.Errorf("range request: %d %q %q", rrec.Code, rrec.Header().Get("Content-Range"), rrec.Body.String())
		}
		if rec := raw("GET", "/v1/storage/themes/hero.txt"+pub); rec.Header().Get("Accept-Ranges") != "bytes" ||
			rec.Header().Get("ETag") == "" || !strings.Contains(strings.Join(rec.Header().Values("Vary"), ","), "apikey") {
			return fmt.Errorf("download validators / Vary: %q %q %q", rec.Header().Get("Accept-Ranges"), rec.Header().Get("ETag"), rec.Header().Values("Vary"))
		}
		// Anonymous download URLs are capped at 1 h.
		if r := call("POST", "/v1/storage/signed-url", env.publicKey, "", `{"key":"themes/hero.txt","operation":"download","expires_in":604800}`); r.code != 200 || !strings.Contains(r.body, "X-Amz-Expires=3600") {
			return fmt.Errorf("anonymous download URL must be capped at 1 h: %w", r)
		}
		if r := call("GET", "/v1/storage/themes/hero.txt?apikey="+env.publicKey, "", "", ""); r.code != 200 || r.body != "hello team" {
			return fmt.Errorf("anonymous caller must read a public-shared file (apikey in the URL): %w", r)
		}
		if r := call("GET", "/v1/storage/?prefix=themes/", env.publicKey, "", ""); r.code != 200 || !strings.Contains(r.body, "themes/hero.txt") {
			return fmt.Errorf("anonymous listing of a public folder: %w", r)
		}
		if r := call("POST", "/v1/storage/signed-url", env.publicKey, "", `{"key":"themes/hero.txt","operation":"download"}`); r.code != 200 {
			return fmt.Errorf("anonymous signed download URL for a public file: %w", r)
		}
		if r := call("POST", "/v1/storage/signed-url", env.publicKey, "", `{"key":"themes/new.txt","operation":"upload","content_type":"text/plain"}`); r.code != http.StatusUnauthorized {
			return fmt.Errorf("anonymous upload URL must be refused: %w", r)
		}
		// Rules listing, then unshare → private again.
		if r := call("GET", sharing+"/", "", env.platformJWT, ""); r.code != 200 || !strings.Contains(r.body, `"public"`) {
			return fmt.Errorf("list rules: %w", r)
		}
		if r := call("DELETE", sharing+"/?prefix=themes/", "", env.platformJWT, ""); r.code != http.StatusNoContent {
			return fmt.Errorf("remove rule: %w", r)
		}
		if r := call("GET", "/v1/storage/themes/hero.txt", env.publicKey, user, ""); r.code != http.StatusNotFound {
			return fmt.Errorf("after unsharing the file must be private again: %w", r)
		}
		return nil
	})

	check(t, env, "console_storage_upload", func() error {
		if r := env.upload("", "", "http://api.eurobase.test/platform/projects/"+env.projectID+"/storage/upload", "e2e/console.txt"); r.code >= 300 {
			return r
		}
		if n := env.dedCount(t, "storage_objects", "key = 'e2e/console.txt'"); n != 1 {
			return fmt.Errorf("storage metadata not on the dedicated DB (count %d)", n)
		}
		return nil
	})

	check(t, env, "console_table_editor_insert", func() error {
		if r := console("POST", "/data/todos", `{"title":"from-console"}`); r.code >= 300 {
			return r
		}
		if n := env.dedCount(t, "todos", "title = 'from-console'"); n != 1 {
			return fmt.Errorf("row not on the dedicated DB (count %d)", n)
		}
		return nil
	})

	check(t, env, "console_sql_editor", func() error {
		if r := console("POST", "/data/sql", `{"sql":"INSERT INTO todos (title) VALUES ('from-sql-editor')"}`); r.code >= 300 {
			return r
		}
		if n := env.dedCount(t, "todos", "title = 'from-sql-editor'"); n != 1 {
			return fmt.Errorf("row not on the dedicated DB (count %d)", n)
		}
		return nil
	})

	check(t, env, "console_table_editor_read_update", func() error {
		r := console("GET", "/data/todos", "")
		if r.code != 200 || !strings.Contains(r.body, "from-console") || strings.Contains(r.body, decoyTitle) {
			return fmt.Errorf("select read the wrong rows: %w", r)
		}
		id := env.dedScalar(t, "SELECT id::text FROM %s WHERE title = 'from-console'", "todos")
		if r := console("PATCH", "/data/todos/"+id, `{"title":"console-updated"}`); r.code >= 300 {
			return r
		}
		if n := env.dedCount(t, "todos", "title = 'console-updated'"); n != 1 {
			return fmt.Errorf("update not on the dedicated DB (count %d)", n)
		}
		return nil
	})

	// MCP runSQLTransaction uses this route too.
	check(t, env, "console_sql_transaction", func() error {
		if r := console("POST", "/data/sql/transaction", `{"statements":["INSERT INTO todos (title) VALUES ('from-tx-1')","INSERT INTO todos (title) VALUES ('from-tx-2')"]}`); r.code >= 300 {
			return r
		}
		if n := env.dedCount(t, "todos", "title LIKE 'from-tx-%'"); n != 2 {
			return fmt.Errorf("rows not on the dedicated DB (count %d)", n)
		}
		return nil
	})

	// The console runs as the dedicated instance's owner (console traffic
	// is service-role by design); the point is that it is *that* instance.
	check(t, env, "console_sql_on_dedicated", func() error {
		r := console("POST", "/data/sql", `{"sql":"SELECT current_database() AS db"}`)
		if r.code != 200 || !strings.Contains(r.body, `"db":"`+env.dedDB+`"`) {
			return fmt.Errorf("not on the dedicated DB: %w", r)
		}
		return nil
	})

	// A Team project whose dedicated pool can't be opened is refused —
	// never served from the shared cluster.
	check(t, env, "console_fails_closed_without_dedicated_pool", func() error {
		reached := false
		mw := tenant.PlatformTenantContext(env.gw, env.dev, func(context.Context, string) *pgxpool.Pool { return nil })
		h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
		cr := chi.NewRouter()
		cr.Handle("/p/{id}/data", h)
		req := httptest.NewRequest("GET", "/p/"+env.projectID+"/data", nil)
		req = req.WithContext(auth.ContextWithClaims(req.Context(), &auth.Claims{Subject: env.ownerUser, Email: env.ownerEmail}))
		rec := httptest.NewRecorder()
		cr.ServeHTTP(rec, req)
		if reached || rec.Code != http.StatusServiceUnavailable {
			return fmt.Errorf("handler reached=%v, status %d %s (want 503, not reached)", reached, rec.Code, rec.Body.String())
		}
		return nil
	})

	check(t, env, "console_vault_set", func() error {
		if r := console("POST", "/vault", `{"name":"E2E_CONSOLE_SECRET","value":"v1"}`); r.code >= 300 {
			return r
		}
		if n := env.dedCount(t, "vault_secrets", "name = 'E2E_CONSOLE_SECRET'"); n != 1 {
			return fmt.Errorf("secret not in the dedicated DB's vault (count %d)", n)
		}
		return nil
	})

	check(t, env, "sdk_vault_set", func() error {
		if r := sdk("POST", "/v1/vault", `{"name":"E2E_SDK_SECRET","value":"v1"}`); r.code >= 300 {
			return r
		}
		if n := env.dedCount(t, "vault_secrets", "name = 'E2E_SDK_SECRET'"); n != 1 {
			return fmt.Errorf("secret not in the dedicated DB's vault (count %d)", n)
		}
		return nil
	})

	// Through the real router: a Team project whose dedicated database is
	// still provisioning (no host yet — its pool can't be opened) gets
	// 503 on tenant-data routes, never the shared cluster, while its
	// platform-table settings stay editable.
	check(t, env, "console_fails_closed_while_provisioning", func() error {
		stuck := env.addProvisioningTeamProject(t)
		for _, path := range []string{"/data/todos", "/users"} {
			r := env.consoleFor(stuck, "GET", path, "")
			if r.code != http.StatusServiceUnavailable {
				return fmt.Errorf("want 503: %w", r)
			}
		}
		body, err := json.Marshal(map[string]any{"auth_config": tenant.DefaultAuthConfig()})
		if err != nil {
			return err
		}
		req := httptest.NewRequest("PATCH", "http://api.eurobase.test/v1/tenants/"+stuck, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+env.platformJWT)
		rec := httptest.NewRecorder()
		env.router.ServeHTTP(rec, req)
		if rec.Code >= 300 {
			return fmt.Errorf("settings locked out: PATCH /v1/tenants/{id}: %d %s", rec.Code, rec.Body.String())
		}
		// SDK DDL for it: refused, never the shared cluster.
		sec := env.addAPIKeys(t, stuck)
		sreq := httptest.NewRequest("POST", "http://api.eurobase.test/v1/db/schema/tables", strings.NewReader(`{"name":"never","columns":[{"name":"id","type":"text"}]}`))
		sreq.Header.Set("apikey", sec)
		sreq.Header.Set("Content-Type", "application/json")
		srec := httptest.NewRecorder()
		env.router.ServeHTTP(srec, sreq)
		if srec.Code != http.StatusServiceUnavailable {
			return fmt.Errorf("SDK DDL while provisioning: want 503, got %d %s", srec.Code, srec.Body.String())
		}
		// End-user auth for it: refused, never the shared users table.
		areq := httptest.NewRequest("POST", "http://api.eurobase.test/v1/auth/signup", strings.NewReader(`{"email":"x@team.test","password":"Correct-horse-9"}`))
		areq.Header.Set("apikey", sec)
		areq.Header.Set("Content-Type", "application/json")
		arec := httptest.NewRecorder()
		env.router.ServeHTTP(arec, areq)
		if arec.Code != http.StatusServiceUnavailable {
			return fmt.Errorf("end-user signup while provisioning: want 503, got %d %s", arec.Code, arec.Body.String())
		}
		lreq := httptest.NewRequest("GET", "http://api.eurobase.test/v1/storage/", nil)
		lreq.Header.Set("apikey", sec)
		lrec := httptest.NewRecorder()
		env.router.ServeHTTP(lrec, lreq)
		if lrec.Code != http.StatusServiceUnavailable {
			return fmt.Errorf("SDK storage while provisioning: want 503, got %d %s", lrec.Code, lrec.Body.String())
		}
		// Saving an OAuth client secret needs the dedicated vault: refused,
		// not written to the shared one.
		cfg := tenant.DefaultAuthConfig()
		cfg.OAuthProviders = map[string]tenant.OAuthProviderConfig{
			"github": {Enabled: true, ClientID: "e2e-client", ClientSecret: "e2e-secret"},
		}
		body, err = json.Marshal(map[string]any{"auth_config": cfg})
		if err != nil {
			return err
		}
		req = httptest.NewRequest("PATCH", "http://api.eurobase.test/v1/tenants/"+stuck, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+env.platformJWT)
		rec = httptest.NewRecorder()
		env.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			return fmt.Errorf("OAuth secret save while provisioning: want 503, got %d %s", rec.Code, rec.Body.String())
		}
		return nil
	})

	// ded_only exists only on the dedicated DB, so a listing read from the
	// stale shared copy can't pass.
	check(t, env, "console_schema_introspection", func() error {
		r := console("GET", "/schema", "")
		if r.code != 200 {
			return r
		}
		if !strings.Contains(r.body, "ded_only") {
			return fmt.Errorf("schema listing lacks the dedicated-only table: %w", r)
		}
		return nil
	})

	// Console Table Editor schema editing (/schema/tables): the DDL itself
	// is routed, but its prechecks and listings read the shared cluster
	// (#679). ded_only exists only on the dedicated DB.
	check(t, env, "console_ddl_create_table", func() error {
		if r := console("POST", "/schema/tables", `{"name":"console_made","columns":[{"name":"id","type":"uuid","primary_key":true,"default":"gen_random_uuid()"},{"name":"note","type":"text"}]}`); r.code >= 300 {
			return r
		}
		if !env.dedTableExists(t, "console_made") {
			return errors.New("table not created on the dedicated DB")
		}
		return nil
	})

	check(t, env, "console_ddl_add_column", func() error {
		if r := console("POST", "/schema/tables/ded_only/columns", `{"name":"note","type":"text","nullable":true}`); r.code >= 300 {
			return r
		}
		if env.dedScalar(t, "SELECT count(*)::text FROM pg_attribute WHERE attrelid = '%s'::regclass AND attname = 'note'", "ded_only") != "1" {
			return errors.New("column not added on the dedicated DB")
		}
		return nil
	})

	check(t, env, "console_ddl_list_indexes", func() error {
		r := console("GET", "/schema/tables/ded_only/indexes", "")
		if r.code != 200 {
			return r
		}
		if !strings.Contains(r.body, "ded_only_marker_idx") {
			return fmt.Errorf("index listing lacks the dedicated-only index: %w", r)
		}
		return nil
	})

	check(t, env, "console_ddl_alter_and_drop_column", func() error {
		if r := console("PATCH", "/schema/tables/ded_only/columns/note", `{"new_name":"note2"}`); r.code >= 300 {
			return r
		}
		if r := console("DELETE", "/schema/tables/ded_only/columns/note2", ""); r.code >= 300 {
			return r
		}
		if env.dedScalar(t, "SELECT count(*)::text FROM pg_attribute WHERE attrelid = '%s'::regclass AND attname IN ('note', 'note2') AND NOT attisdropped", "ded_only") != "0" {
			return errors.New("column not renamed/dropped on the dedicated DB")
		}
		return nil
	})

	check(t, env, "console_ddl_list_functions_and_policies", func() error {
		r := console("GET", "/schema/functions", "")
		if r.code != 200 || !strings.Contains(r.body, "ded_only_fn") {
			return fmt.Errorf("function listing lacks the dedicated-only function: %w", r)
		}
		if strings.Contains(r.body, "auth_uid") {
			return fmt.Errorf("function listing shows platform helpers: %w", r)
		}
		r = console("GET", "/schema/tables/ded_only/policies", "")
		if r.code != 200 || !strings.Contains(r.body, "ded_only_policy") {
			return fmt.Errorf("policy listing lacks the dedicated-only policy: %w", r)
		}
		return nil
	})

	check(t, env, "console_function_reserved_names", func() error {
		r := console("POST", "/schema/functions", `{"name":"auth_uid","body":"SELECT NULL::uuid","returns":"uuid","language":"sql"}`)
		if r.code < 400 || !strings.Contains(r.body, "reserved") {
			return fmt.Errorf("overwriting the RLS helper auth_uid must be refused: %w", r)
		}
		return nil
	})

	check(t, env, "console_rls_audit", func() error {
		r := console("GET", "/schema/rls-audit", "")
		if r.code != 200 || !strings.Contains(r.body, "ded_only") {
			return fmt.Errorf("RLS audit lacks the dedicated-only table: %w", r)
		}
		return nil
	})

	check(t, env, "console_schema_changes_backfill", func() error {
		// Earlier checks log DDL on ded_only themselves; only the backfill
		// writes a create_table entry for it (it was created outside the
		// DDL handlers), and only if it listed the dedicated DB's tables.
		r := console("GET", "/schema/changes", "")
		if r.code != 200 {
			return r
		}
		var changes []struct {
			Action    string          `json:"action"`
			TableName string          `json:"table_name"`
			Detail    json.RawMessage `json:"detail"`
		}
		if err := json.Unmarshal([]byte(r.body), &changes); err != nil {
			return fmt.Errorf("decode: %v: %w", err, r)
		}
		for _, c := range changes {
			if c.Action == "create_table" && c.TableName == "ded_only" && strings.Contains(string(c.Detail), "backfill") {
				return nil
			}
		}
		return fmt.Errorf("no backfilled create_table entry for the dedicated-only table: %w", r)
	})

	check(t, env, "sdk_ddl_create_table", func() error {
		if r := sdk("POST", "/v1/db/schema/tables", `{"name":"sdk_made","columns":[{"name":"id","type":"uuid","primary_key":true,"default":"gen_random_uuid()"},{"name":"note","type":"text"}]}`); r.code >= 300 {
			return r
		}
		if !env.dedTableExists(t, "sdk_made") {
			return errors.New("table not created on the dedicated DB")
		}
		return nil
	})

	// #682: the read-only login (console Connection page) is capped.
	check(t, env, "readonly_connection_limit", func() error {
		var limit int
		if err := env.ded.QueryRow(context.Background(),
			`SELECT rolconnlimit FROM pg_roles WHERE rolname = 'eurobase_readonly'`).Scan(&limit); err != nil {
			return err
		}
		if limit != dbprovider.ReadonlyConnLimit {
			return fmt.Errorf("eurobase_readonly CONNECTION LIMIT = %d, want %d", limit, dbprovider.ReadonlyConnLimit)
		}
		return nil
	})

	// Background token cleanup (#681): expired tokens on the dedicated DB
	// are deleted, fresh ones kept; the shared cluster's stale copy of an
	// upgraded project is left alone.
	check(t, env, "token_cleanup_on_dedicated", func() error {
		ctx := context.Background()
		rt := pgx.Identifier{env.schema, "refresh_tokens"}.Sanitize()
		users := pgx.Identifier{env.schema, "users"}.Sanitize()
		for _, q := range []string{
			`INSERT INTO ` + rt + ` (user_id, token_hash, expires_at)
			   SELECT id, 'e2e-expired', now() - interval '30 days' FROM ` + users + ` WHERE email = 'enduser@team.test'`,
			`INSERT INTO ` + rt + ` (user_id, token_hash, expires_at)
			   SELECT id, 'e2e-fresh', now() + interval '1 day' FROM ` + users + ` WHERE email = 'enduser@team.test'`,
		} {
			if _, err := env.ded.Exec(ctx, q); err != nil {
				return err
			}
		}
		if env.upgraded {
			// A stale-copy user + expired token on the shared cluster.
			if _, err := env.shared.Exec(ctx, `WITH u AS (INSERT INTO `+users+` (email) VALUES ('stale@team.test') RETURNING id)
				INSERT INTO `+rt+` (user_id, token_hash, expires_at) SELECT id, 'e2e-stale', now() - interval '30 days' FROM u`); err != nil {
				return err
			}
		}
		// Skip path first (Team): the dedicated database can't be opened —
		// the project is skipped, nothing is deleted anywhere.
		if env.scenario == "free" {
			goto realRun
		}
		cleanupExpiredTokens(ctx, env.gw, func(context.Context, string) (*pgxpool.Pool, error) {
			return nil, errors.New("simulated: dedicated database unavailable")
		})
		if n := env.dedCount(t, "refresh_tokens", "token_hash = 'e2e-expired'"); n != 1 {
			return fmt.Errorf("skip path: dedicated token touched (count %d)", n)
		}
		if env.upgraded {
			var n int
			if err := env.shared.QueryRow(ctx, `SELECT count(*) FROM `+rt+` WHERE token_hash = 'e2e-stale'`).Scan(&n); err != nil {
				return err
			}
			if n != 1 {
				return fmt.Errorf("skip path: cleanup fell back to the shared cluster's stale copy (count %d)", n)
			}
		}
	realRun:
		repo := dbprovider.NewRepo(env.shared)
		cleanupExpiredTokens(ctx, env.gw, func(ctx context.Context, projectID string) (*pgxpool.Pool, error) {
			p, err := dbprovider.OpenOwnerPool(ctx, repo, env.cipher, projectID)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, nil
			}
			return p, err
		})
		if n := env.dedCount(t, "refresh_tokens", "token_hash = 'e2e-expired'"); n != 0 {
			return fmt.Errorf("expired token not deleted on the dedicated DB (count %d)", n)
		}
		if n := env.dedCount(t, "refresh_tokens", "token_hash = 'e2e-fresh'"); n != 1 {
			return fmt.Errorf("fresh token deleted on the dedicated DB (count %d)", n)
		}
		if env.upgraded {
			var n int
			if err := env.shared.QueryRow(ctx, `SELECT count(*) FROM `+rt+` WHERE token_hash = 'e2e-stale'`).Scan(&n); err != nil {
				return err
			}
			if n != 1 {
				return fmt.Errorf("cleanup touched the shared cluster's stale copy (count %d)", n)
			}
		}
		return nil
	})

	// Edge functions (#676): the runner asks runner_get_tenant_db where the
	// project's data lives, then logs in there as <schema>_func with the
	// derived password (the worker's tenantlogin pass sets it: EnsureAll on
	// the shared cluster, EnsureTeam — per-instance password — on dedicated
	// instances).
	check(t, env, "function_login_routed", func() error {
		ctx := context.Background()
		secret := []byte(randHexT(t, 32))
		rows, err := env.runnerRoute(ctx, env.projectID)
		if err != nil {
			return err
		}
		ensurer, err := tenantlogin.NewEnsurer(env.dev, "eurobase", secret)
		if err != nil {
			return err
		}
		// As cmd/worker wires it.
		ensurer.PrepareDedicated = func(ctx context.Context, pool *pgxpool.Pool, schema string) error {
			ready, err := dbprovider.FuncRoleReady(ctx, pool, schema)
			if err != nil || ready {
				return err
			}
			return dbprovider.EnsureFuncRole(ctx, pool, schema)
		}
		role := tenantlogin.FuncRole(env.schema)
		var loginURL url.URL
		password := tenantlogin.FuncPassword(secret, env.schema)
		if env.scenario == "free" {
			if len(rows) != 0 {
				return fmt.Errorf("runner_get_tenant_db routed a Free project away from the shared cluster: %+v", rows)
			}
			if _, err := ensurer.EnsureAll(ctx); err != nil {
				return fmt.Errorf("EnsureAll: %w", err)
			}
			u, err := url.Parse(env.shared.Config().ConnString())
			if err != nil {
				return err
			}
			loginURL = *u
		} else {
			if len(rows) != 1 || rows[0].state != "active" || rows[0].db != env.dedDB || rows[0].id == "" {
				return fmt.Errorf("runner_get_tenant_db = %+v, want the active dedicated database %s", rows, env.dedDB)
			}
			repo := dbprovider.NewRepo(env.shared)
			open := func(ctx context.Context, projectID string) (*pgxpool.Pool, tenantlogin.DedicatedDB, error) {
				if projectID != env.projectID {
					return nil, tenantlogin.DedicatedDB{}, nil // other scenarios' projects (their own cipher)
				}
				rec, err := repo.GetLiveByProject(ctx, projectID)
				if err != nil {
					return nil, tenantlogin.DedicatedDB{}, err
				}
				p, err := dbprovider.OpenOwnerPoolFor(ctx, rec, env.cipher)
				return p, tenantlogin.DedicatedDB{ID: rec.ID, Database: rec.DatabaseName}, err
			}
			if env.upgraded {
				// An instance bootstrapped before #676: no function role, no
				// vault helper. The worker's pass (PrepareDedicated) adds them.
				for _, q := range []string{
					`DROP FUNCTION ` + pgx.Identifier{env.schema}.Sanitize() + `.runner_vault_get(text)`,
					`DROP FUNCTION public.ensure_tenant_func_role(text)`,
					`DROP OWNED BY ` + pgx.Identifier{role}.Sanitize(),
					`DROP ROLE ` + pgx.Identifier{role}.Sanitize(),
				} {
					if _, err := env.ded.Exec(ctx, q); err != nil {
						return fmt.Errorf("%s: %w", q, err)
					}
				}
			}
			// The first pass sets LOGIN but can't grant CONNECT (the owner
			// doesn't own the database, as on Scaleway): it says so.
			if _, err := ensurer.EnsureTeam(ctx, open); !errors.Is(err, tenantlogin.ErrNoConnect) {
				return fmt.Errorf("EnsureTeam without CONNECT: err = %v, want ErrNoConnect", err)
			}
			// With the provider hook the pass completes by itself. Stand-in
			// for Scaleway's SetPrivilege readwrite, run by the instance
			// superuser: CONNECT plus more than the shared cluster grants —
			// the lockdown (EnsureFuncRole) must strip the extras.
			ensurer.GrantDedicatedConnect = func(ctx context.Context, projectID, schema string, pool *pgxpool.Pool) error {
				if projectID != env.projectID || schema != env.schema {
					return fmt.Errorf("grant for %s/%s, want %s/%s", projectID, schema, env.projectID, env.schema)
				}
				r, s := pgx.Identifier{role}.Sanitize(), pgx.Identifier{env.schema}.Sanitize()
				for _, q := range []string{
					`GRANT CONNECT ON DATABASE ` + pgx.Identifier{env.dedDB}.Sanitize() + ` TO ` + r,
					`GRANT ALL ON ALL TABLES IN SCHEMA ` + s + ` TO ` + r,
					`GRANT ALL ON ALL SEQUENCES IN SCHEMA ` + s + ` TO ` + r,
					`GRANT CREATE ON SCHEMA ` + s + ` TO ` + r,
				} {
					if _, err := env.ded.Exec(ctx, q); err != nil {
						return fmt.Errorf("%s: %w", q, err)
					}
				}
				return dbprovider.EnsureFuncRole(ctx, pool, schema)
			}
			if n, err := ensurer.EnsureTeam(ctx, open); err != nil || n != 1 {
				return fmt.Errorf("EnsureTeam: ensured %d, err %v", n, err)
			}
			loginURL = url.URL{Scheme: "postgres", Host: fmt.Sprintf("%s:%d", rows[0].host, rows[0].port), Path: "/" + rows[0].db}
			loginURL.RawQuery = "sslmode=require"
			password = tenantlogin.FuncPassword(secret, tenantlogin.DedicatedSubject(rows[0].id, env.schema))
			// The shared-cluster password doesn't open the dedicated role.
			bad := loginURL
			bad.User = url.UserPassword(role, tenantlogin.FuncPassword(secret, env.schema))
			if c, err := pgx.Connect(ctx, bad.String()); err == nil {
				c.Close(ctx)
				return fmt.Errorf("the shared-cluster password logs in on the dedicated instance")
			}
		}
		loginURL.User = url.UserPassword(role, password)
		conn, err := pgx.Connect(ctx, loginURL.String())
		if err != nil {
			return fmt.Errorf("login as %s: %w", role, err)
		}
		defer conn.Close(ctx)

		var limit int
		if err := conn.QueryRow(ctx, `SELECT rolconnlimit FROM pg_roles WHERE rolname = current_user`).Scan(&limit); err != nil {
			return err
		}
		wantLimit := tenantlogin.DedicatedFuncConnLimit
		if env.scenario == "free" {
			wantLimit = tenantlogin.FuncConnLimit
		}
		if limit != wantLimit {
			return fmt.Errorf("%s CONNECTION LIMIT = %d, want %d", role, limit, wantLimit)
		}
		// Data the function sees: the project's own tables, including one
		// created after provisioning (default privileges), and on Team the
		// marker that only the dedicated database has.
		for _, tbl := range []string{"todos", "ded_only"} {
			if _, err := conn.Exec(ctx, `SELECT count(*) FROM `+pgx.Identifier{env.schema, tbl}.Sanitize()); err != nil {
				return fmt.Errorf("function role reading %s: %w", tbl, err)
			}
		}
		// Exactly the shared cluster's privileges: DML, no TRUNCATE (which
		// skips RLS) / REFERENCES / TRIGGER, no setval, no CREATE.
		var extra string
		if err := conn.QueryRow(ctx, `
			SELECT coalesce(string_agg(x, ', '), '') FROM (
			  SELECT c.relname || ':' || p AS x
			    FROM pg_class c, unnest(ARRAY['TRUNCATE','REFERENCES','TRIGGER']) p
			   WHERE c.relnamespace = $1::regnamespace
			     AND CASE WHEN c.relkind = 'r' THEN has_table_privilege(c.oid, p) ELSE false END
			  UNION ALL
			  SELECT c.relname || ':UPDATE' FROM pg_class c
			   WHERE c.relnamespace = $1::regnamespace
			     AND CASE WHEN c.relkind = 'S' THEN has_sequence_privilege(c.oid, 'UPDATE') ELSE false END
			  UNION ALL
			  SELECT 'schema:CREATE' WHERE has_schema_privilege($1, 'CREATE')) q`, env.schema).Scan(&extra); err != nil {
			return err
		}
		if extra != "" {
			return fmt.Errorf("function role has privileges beyond the shared cluster's: %s", extra)
		}
		if env.scenario == "free" {
			return nil // shared vault: public.vault_get_for_runner (runner's own login)
		}
		// ctx.vault on Team: <schema>.runner_vault_get returns the sealed
		// row (per-tenant key) from the dedicated vault; the table itself
		// stays RLS-hidden.
		var enc, want []byte
		var kv *int16
		if err := conn.QueryRow(ctx, `SELECT encrypted, key_version FROM `+pgx.Identifier{env.schema}.Sanitize()+`.runner_vault_get($1)`,
			"E2E_CONSOLE_SECRET").Scan(&enc, &kv); err != nil {
			return fmt.Errorf("runner_vault_get: %w", err)
		}
		if err := env.ded.QueryRow(ctx, `SELECT secret FROM `+pgx.Identifier{env.schema, "vault_secrets"}.Sanitize()+` WHERE name = 'E2E_CONSOLE_SECRET'`).Scan(&want); err != nil {
			return err
		}
		if !bytes.Equal(enc, want) {
			return fmt.Errorf("runner_vault_get returned a different secret than the dedicated vault holds")
		}
		if kv == nil || *kv < 1 {
			return fmt.Errorf("dedicated vault secret has key_version %v; the runner only accepts per-tenant sealed (>= 1)", kv)
		}
		var direct int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{env.schema, "vault_secrets"}.Sanitize()).Scan(&direct); err != nil {
			return err
		}
		if direct != 0 {
			return fmt.Errorf("function role reads vault_secrets directly (%d rows)", direct)
		}
		return nil
	})

	// runner_get_tenant_db while data is moving: the runner gets "not
	// ready" (no host), never a route — and only its own login may call it.
	check(t, env, "function_route_not_ready", func() error {
		ctx := context.Background()
		want := func(projectID, state string) error {
			rows, err := env.runnerRoute(ctx, projectID)
			if err != nil {
				return err
			}
			if len(rows) != 1 || rows[0].state != state || rows[0].host != "" {
				return fmt.Errorf("runner_get_tenant_db(%s) = %+v, want state %q without a host", projectID, rows, state)
			}
			return nil
		}
		// An upgrade in flight — for any project, before its dedicated row
		// exists too (Free/Pro).
		var upID string
		if err := env.shared.QueryRow(ctx, `INSERT INTO project_upgrades (project_id, from_plan, to_plan, state)
			VALUES ($1, 'pro', 'team', 'copying') RETURNING id`, env.projectID).Scan(&upID); err != nil {
			return err
		}
		err := want(env.projectID, "upgrading")
		if _, derr := env.shared.Exec(ctx, `DELETE FROM project_upgrades WHERE id = $1`, upID); derr != nil && err == nil {
			err = derr
		}
		if err != nil {
			return err
		}
		if env.scenario == "free" {
			// Maintenance on a shared-cluster project doesn't change its route.
			if _, err := env.shared.Exec(ctx, `UPDATE projects SET maintenance_mode = true WHERE id = $1`, env.projectID); err != nil {
				return err
			}
			rows, err := env.runnerRoute(ctx, env.projectID)
			_, _ = env.shared.Exec(ctx, `UPDATE projects SET maintenance_mode = false WHERE id = $1`, env.projectID)
			if err != nil || len(rows) != 0 {
				return fmt.Errorf("free project in maintenance: rows %+v err %v, want none (shared)", rows, err)
			}
		} else {
			if err := want(env.addProvisioningTeamProject(t), "provisioning"); err != nil {
				return err
			}
			if _, err := env.shared.Exec(ctx, `UPDATE projects SET maintenance_mode = true WHERE id = $1`, env.projectID); err != nil {
				return err
			}
			err := want(env.projectID, "maintenance")
			if _, uerr := env.shared.Exec(ctx, `UPDATE projects SET maintenance_mode = false WHERE id = $1`, env.projectID); uerr != nil && err == nil {
				err = uerr
			}
			if err != nil {
				return err
			}
			var rID string
			if err := env.shared.QueryRow(ctx, `INSERT INTO restore_operations (project_id, kind, source_ref, state, old_instance_id)
				SELECT $1, 'snapshot', 'e2e', 'cutover', id FROM project_databases
				 WHERE project_id = $1 AND deleted_at IS NULL LIMIT 1 RETURNING id`, env.projectID).Scan(&rID); err != nil {
				return err
			}
			err = want(env.projectID, "restoring")
			if _, derr := env.shared.Exec(ctx, `DELETE FROM restore_operations WHERE id = $1`, rID); derr != nil && err == nil {
				err = derr
			}
			if err != nil {
				return err
			}
		}
		// Only the runner's login may ask.
		for _, r := range []string{"eurobase_gateway", tenantlogin.FuncRole(env.schema)} {
			tx, err := env.shared.Begin(ctx)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `SET LOCAL ROLE `+pgx.Identifier{r}.Sanitize())
			if err == nil {
				_, err = tx.Exec(ctx, `SELECT * FROM public.runner_get_tenant_db($1::uuid)`, env.projectID)
			}
			_ = tx.Rollback(ctx)
			var pgErr *pgconn.PgError
			if env.scenario != "free" && r != "eurobase_gateway" {
				// A Team project has no <schema>_func on the shared cluster.
				continue
			}
			if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
				return fmt.Errorf("runner_get_tenant_db as %s: err %v, want 42501", r, err)
			}
		}
		return nil
	})

	// Edge functions through the real Deno runner (#676): an invocation
	// via the SDK route runs ctx.db.sql / ctx.vault.get / ctx.storage on the
	// project's own database — dedicated for Team, shared for Free — as
	// <schema>_func, and the storage callback records the object there.
	check(t, env, "function_invoke_real_runner", func() error {
		if !env.runnerOn {
			t.Skip("no deno (TEAM_TEST_DENO); CI runs this with TEAM_TEST_REQUIRE_RUNNER=1")
		}
		ctx := context.Background()
		// The worker's login pass, with the runner's secret.
		if err := env.ensureFuncLogins(ctx); err != nil {
			return err
		}

		code := `export default async function handler(req, ctx) {
  var who = await ctx.db.sql("SELECT current_database() AS db, current_user AS role");
  var ins = await ctx.db.sql("INSERT INTO todos (title) VALUES ($1) RETURNING id", ["from the runner"]);
  var secret = await ctx.vault.get("E2E_CONSOLE_SECRET");
  var up = await ctx.storage.upload("fn/runner.txt", "hello from the runner", { contentType: "text/plain" });
  return new Response(JSON.stringify({ db: who[0].db, role: who[0].role, todo: ins[0].id, secret: secret, upload: up }),
    { headers: { "Content-Type": "application/json" } });
}`
		// User-less invocation (service context), so no end-user JWT.
		body, _ := json.Marshal(map[string]any{"name": "e2e-runner", "code": code, "verify_jwt": false})
		if r := console("POST", "/functions", string(body)); r.code >= 300 {
			return r
		}
		r := sdk("POST", "/v1/functions/e2e-runner", `{}`)
		if r.code != http.StatusOK {
			return fmt.Errorf("invoke: %d %s", r.code, r.body)
		}
		var got struct {
			DB, Role, Todo, Secret string
			Upload                 struct{ Key string }
		}
		if err := json.Unmarshal([]byte(r.body), &got); err != nil {
			return fmt.Errorf("invoke response %q: %w", r.body, err)
		}
		wantDB := env.dedDB
		if env.scenario == "free" {
			wantDB = "eurobase"
		}
		if got.DB != wantDB || got.Role != tenantlogin.FuncRole(env.schema) {
			return fmt.Errorf("ran on %s as %s, want %s as %s", got.DB, got.Role, wantDB, tenantlogin.FuncRole(env.schema))
		}
		if got.Secret != "v1" {
			return fmt.Errorf("ctx.vault.get = %q, want the project's secret", got.Secret)
		}
		if n := env.dedCount(t, "todos", fmt.Sprintf("id = '%s'", got.Todo)); n != 1 {
			return fmt.Errorf("ctx.db.sql row not in the project's database (count %d)", n)
		}
		if got.Upload.Key != "fn/runner.txt" {
			return fmt.Errorf("ctx.storage.upload returned %+v", got.Upload)
		}
		if n := env.dedCount(t, "storage_objects", "key = 'fn/runner.txt'"); n != 1 {
			return fmt.Errorf("function upload not tracked in the project's storage_objects (count %d)", n)
		}
		// A user-less invocation's upload is a developer file (NULL owner),
		// so shared folders (#697) cover it.
		if owner := env.dedScalar(t, "SELECT coalesce(uploaded_by::text, '<null>') FROM %s WHERE key = 'fn/runner.txt'", "storage_objects"); owner != "<null>" {
			return fmt.Errorf("user-less function upload recorded with owner %q, want NULL", owner)
		}
		if ok, err := env.s3.ObjectExists(ctx, env.bucket, "fn/runner.txt"); err != nil || !ok {
			return fmt.Errorf("function upload not in S3: %v %v", ok, err)
		}
		// A user-less upload never takes an existing file away from its
		// end user (e.g. a webhook rewriting users/<id>/…).
		var endUser string
		if err := env.ded.QueryRow(ctx, `SELECT id::text FROM `+pgx.Identifier{env.schema, "users"}.Sanitize()+` ORDER BY created_at LIMIT 1`).Scan(&endUser); err != nil {
			return fmt.Errorf("pick an end user: %w", err)
		}
		if _, err := env.ded.Exec(ctx, `UPDATE `+pgx.Identifier{env.schema, "storage_objects"}.Sanitize()+` SET uploaded_by = $1 WHERE key = 'fn/runner.txt'`, endUser); err != nil {
			return err
		}
		if r := sdk("POST", "/v1/functions/e2e-runner", `{}`); r.code != http.StatusOK {
			return fmt.Errorf("second invoke: %d %s", r.code, r.body)
		}
		if owner := env.dedScalar(t, "SELECT coalesce(uploaded_by::text, '<null>') FROM %s WHERE key = 'fn/runner.txt'", "storage_objects"); owner != endUser {
			return fmt.Errorf("user-less re-upload changed the owner to %q, want the end user %s", owner, endUser)
		}
		return nil
	})

	// ctx.storage for a Team project whose database can't be used (here:
	// still provisioning) is refused before S3 — never recorded on the
	// shared cluster, never left untracked.
	check(t, env, "function_storage_refused_without_dedicated_db", func() error {
		ctx := context.Background()
		stuck := env.addProvisioningTeamProject(t)
		body := []byte("x")
		req := httptest.NewRequest("POST", "http://api.eurobase.test/internal/functions/storage/upload", bytes.NewReader(body))
		req.Header.Set("X-Project-ID", stuck)
		req.Header.Set("X-Schema-Name", "tenant_"+strings.ReplaceAll(stuck, "-", "_"))
		req.Header.Set("X-Storage-Key", "fn/stuck.txt")
		req.Header.Set("Content-Type", "text/plain")
		functions.SignStorage(env.runnerHMAC, req.Header, functions.StorageOpUpload, functions.SHA256Hex(body), time.Now())
		rec := httptest.NewRecorder()
		env.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			return fmt.Errorf("upload for a provisioning Team project: %d %s, want 503", rec.Code, rec.Body.String())
		}
		var slug string
		if err := env.shared.QueryRow(ctx, `SELECT slug FROM projects WHERE id = $1`, stuck).Scan(&slug); err != nil {
			return err
		}
		if ok, _ := env.s3.ObjectExists(ctx, "eurobase-"+slug, "fn/stuck.txt"); ok {
			return fmt.Errorf("object reached S3 before the refusal")
		}
		return nil
	})

	// Cron sql / rpc (#677): a scheduled job and the console's Test Run
	// run on the project's own database as <schema>_func — dedicated for
	// Team, shared for Free — with run_as honoured; never the shared
	// cluster for a Team project.
	check(t, env, "cron_sql_rpc_routed", func() error {
		ctx := context.Background()
		if err := env.ensureFuncLogins(ctx); err != nil {
			return err
		}
		// Test Run through the router: sql rolls back, rpc runs.
		r := console("POST", "/cron/test", `{"action_type":"sql","action":"INSERT INTO todos (title) VALUES ('cron dry run')","run_as":"service"}`)
		if r.code != http.StatusOK || !strings.Contains(r.body, `"rows_affected":1`) {
			return fmt.Errorf("sql test run: %d %s", r.code, r.body)
		}
		if n := env.dedCount(t, "todos", "title = 'cron dry run'"); n != 0 {
			return fmt.Errorf("test run wasn't rolled back (%d rows)", n)
		}
		if r := console("POST", "/cron/test", `{"action_type":"rpc","action":"ded_only_fn"}`); r.code != http.StatusOK {
			return fmt.Errorf("rpc test run (a function only the project's own database has): %d %s", r.code, r.body)
		}
		// A scheduled job, run by the worker's executor.
		if r := console("POST", "/cron", `{"name":"e2e-cron","schedule":"* * * * *","action_type":"sql","action":"INSERT INTO todos (title) VALUES ('from cron')","run_as":"service"}`); r.code >= 300 {
			return r
		}
		defer env.shared.Exec(ctx, `UPDATE cron_jobs SET enabled = false WHERE project_id = $1`, env.projectID) //nolint:errcheck
		exec := cron.NewExecutor(cron.NewCronService(env.gw), env.gw).
			WithTenantLogins(env.gw.Config().ConnConfig, env.funcSecret).
			WithTenantRouting(env.dev)
		if err := exec.RunDueJobs(ctx); err != nil {
			return fmt.Errorf("RunDueJobs: %w", err)
		}
		var status, errText string
		if err := env.shared.QueryRow(ctx, `SELECT r.status, coalesce(r.error, '') FROM cron_job_runs r JOIN cron_jobs j ON j.id = r.job_id
			WHERE j.project_id = $1 AND j.name = 'e2e-cron' ORDER BY r.started_at DESC LIMIT 1`, env.projectID).Scan(&status, &errText); err != nil {
			return fmt.Errorf("cron run record: %w", err)
		}
		if status != "success" {
			return fmt.Errorf("cron run %s: %s", status, errText)
		}
		if n := env.dedCount(t, "todos", "title = 'from cron'"); n != 1 {
			return fmt.Errorf("cron row not in the project's database (count %d)", n)
		}
		return nil
	})

	// A Team project whose database isn't serving: the Test Run is
	// refused with the not-ready message, never run on the shared cluster.
	check(t, env, "cron_refused_without_dedicated_db", func() error {
		stuck := env.addProvisioningTeamProject(t)
		r := env.consoleFor(stuck, "POST", "/cron/test", `{"action_type":"sql","action":"SELECT 1"}`)
		if r.code == http.StatusOK || !strings.Contains(r.body, "isn't available right now") {
			return fmt.Errorf("test run for a provisioning Team project: %d %s — want the not-ready refusal", r.code, r.body)
		}
		return nil
	})

	// Connection page (#698): a read-only request gets the SELECT-only
	// login or nothing — never the owner credential, even while the
	// read-only slot is still empty.
	check(t, env, "console_connection_roles", func() error {
		ctx := context.Background()
		type connResp struct {
			URL, Username, Role, Code string
		}
		get := func(role string) (*httpResult, connResp, error) {
			r := console("GET", "/connection?role="+role, "")
			var c connResp
			if r.code == http.StatusOK || r.code == http.StatusConflict {
				if err := json.Unmarshal([]byte(r.body), &c); err != nil {
					return r, c, fmt.Errorf("%s: %w (%s)", r.req, err, r.body)
				}
			}
			return r, c, nil
		}
		r, c, err := get("readonly")
		if err != nil {
			return err
		}
		if r.code != http.StatusOK || c.Role != "readonly" || c.Username != "eurobase_readonly" {
			return fmt.Errorf("readonly: %d role %q user %q (%s)", r.code, c.Role, c.Username, r.body)
		}
		// Bootstrap not finished: empty the read-only slot.
		var roUser string
		var roCT, roNonce []byte
		var roVer int16
		if err := env.shared.QueryRow(ctx, `SELECT readonly_username, readonly_password_ciphertext, readonly_password_nonce, readonly_password_key_version
			FROM project_databases WHERE project_id = $1 AND deleted_at IS NULL`, env.projectID).Scan(&roUser, &roCT, &roNonce, &roVer); err != nil {
			return err
		}
		if _, err := env.shared.Exec(ctx, `UPDATE project_databases SET readonly_username = NULL, readonly_password_ciphertext = NULL,
			readonly_password_nonce = NULL, readonly_password_key_version = NULL WHERE project_id = $1 AND deleted_at IS NULL`, env.projectID); err != nil {
			return err
		}
		defer func() {
			if _, err := env.shared.Exec(ctx, `UPDATE project_databases SET readonly_username = $2, readonly_password_ciphertext = $3,
				readonly_password_nonce = $4, readonly_password_key_version = $5 WHERE project_id = $1 AND deleted_at IS NULL`,
				env.projectID, roUser, roCT, roNonce, roVer); err != nil {
				t.Errorf("restore read-only slot (later checks would run without it): %v", err)
			}
		}()
		r, c, err = get("readonly")
		if err != nil {
			return err
		}
		if r.code != http.StatusConflict || c.Code != "readonly_pending" || c.URL != "" || c.Username != "" || strings.Contains(r.body, "postgres://") {
			return fmt.Errorf("readonly while pending: %d %s — want 409 readonly_pending without a credential", r.code, r.body)
		}
		// Read/write is still available, and says what it is.
		r, c, err = get("readwrite")
		if err != nil {
			return err
		}
		if r.code != http.StatusOK || c.Role != "readwrite" || c.URL == "" {
			return fmt.Errorf("readwrite: %d role %q (%s)", r.code, c.Role, r.body)
		}
		return nil
	})

	// Runs last: nothing above may have touched the shared cluster.
	check(t, env, "shared_cluster_untouched", func() error {
		ctx := context.Background()
		var exists bool
		if err := env.shared.QueryRow(ctx, `SELECT to_regnamespace($1) IS NOT NULL`, env.schema).Scan(&exists); err != nil {
			return err
		}
		if !env.upgraded {
			if exists {
				return fmt.Errorf("tenant schema %s was created on the shared cluster", env.schema)
			}
			return nil
		}
		var fp string
		q := fmt.Sprintf(sharedFingerprintSQL, pgx.Identifier{env.schema, "todos"}.Sanitize(), pgx.Identifier{env.schema, "vault_secrets"}.Sanitize(), pgx.Identifier{env.schema, "storage_objects"}.Sanitize())
		if err := env.shared.QueryRow(ctx, q, env.schema).Scan(&fp); err != nil {
			return err
		}
		if fp != env.sharedFingerprint {
			return fmt.Errorf("stale shared copy changed:\nbefore %s\nafter  %s", env.sharedFingerprint, fp)
		}
		return nil
	})
}

// knownGap: a check expected to fail until issue is fixed, and the error
// text it fails with today (any of sigs). Keyed by check name, or by
// "<scenario>/<name>" for a gap in one scenario only. The sigs cover both
// scenarios: fresh fails on the missing shared schema, upgraded lands on
// the stale shared copy.
type knownGap struct {
	issue string
	sigs  []string
}

// teamOnlyChecks don't apply to a Free / Pro project (shared cluster).
var teamOnlyChecks = map[string]bool{
	"console_fails_closed_without_dedicated_pool":   true,
	"console_fails_closed_while_provisioning":       true,
	"readonly_connection_limit":                     true,
	"shared_cluster_untouched":                      true,
	"credential_reseal":                             true,
	"console_connection_roles":                      true,
	"function_storage_refused_without_dedicated_db": true,
	"cron_refused_without_dedicated_db":             true,
}

var teamKnownGaps = map[string]knownGap{}

func check(t *testing.T, env *teamEnv, name string, fn func() error) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		if env.scenario == "free" && teamOnlyChecks[name] {
			t.Skip("Team only (dedicated database)")
		}
		err := fn()
		var hr *httpResult
		if errors.As(err, &hr) && hr.neverReachedHandler() {
			// A harness auth problem must not hide behind a known gap.
			t.Fatalf("request never reached the handler: %v", err)
		}
		gap, known := teamKnownGaps[name]
		if !known {
			gap, known = teamKnownGaps[env.scenario+"/"+name]
		}
		switch {
		case err == nil && known:
			t.Fatalf("gap %s appears fixed — remove %q from teamKnownGaps", gap.issue, name)
		case err == nil:
		case known && gap.matches(err):
			t.Skipf("known gap %s: %v", gap.issue, err)
		case known:
			t.Fatalf("known gap %s failed differently than expected: %v", gap.issue, err)
		default:
			t.Fatal(err)
		}
	})
}

func (g knownGap) matches(err error) bool {
	for _, s := range g.sigs {
		if strings.Contains(err.Error(), s) {
			return true
		}
	}
	return false
}

// httpResult is a response that failed a check; it carries the status so
// check can tell "reached the handler and misrouted" from "never got in".
type httpResult struct {
	req  string
	code int
	body string
}

func (r *httpResult) Error() string {
	b := r.body
	if len(b) > 300 {
		b = b[:300] + "…"
	}
	return fmt.Sprintf("%s: %d %s", r.req, r.code, strings.TrimSpace(b))
}

func (r *httpResult) neverReachedHandler() bool {
	switch r.code {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusServiceUnavailable:
		return true
	case http.StatusNotFound:
		return strings.Contains(r.body, "project not found")
	}
	return false
}

const decoyTitle = "decoy-on-shared"

// sharedFingerprintSQL summarises the stale shared copy: its tables and
// the todos rows, the vault secret names and the storage object keys
// (%[1]s / %[2]s / %[3]s = the quoted todos / vault_secrets /
// storage_objects tables).
const sharedFingerprintSQL = `
SELECT (SELECT string_agg(relname, ',' ORDER BY relname) FROM pg_class
         WHERE relnamespace = to_regnamespace($1) AND relkind = 'r')
    || ' | ' ||
       (SELECT coalesce(string_agg(title, ',' ORDER BY title), '') FROM ` + "%[1]s" + `)
    || ' | ' ||
       (SELECT coalesce(string_agg(name, ',' ORDER BY name), '') FROM ` + "%[2]s" + `)
    || ' | ' ||
       (SELECT coalesce(string_agg(key, ',' ORDER BY key), '') FROM ` + "%[3]s" + `)`

type teamTestConfig struct {
	sharedAdmin, sharedGW, sharedDev string
	sharedRunner                     string // eurobase_function_runner's login (the real Deno runner)
	deno                             string // path to deno; "" = runner checks skip (fail with TEAM_TEST_REQUIRE_RUNNER=1)
	dedOwner, dedAdmin               string // URLs; the database is replaced per scenario
	runtimePW, readonlyPW            string
	s3Endpoint, s3Key, s3Secret      string
}

type teamEnv struct {
	// router runs the production middleware chain (no devMode, which
	// swaps the SDK chain and skips console membership/role checks):
	// SDK requests carry the secret API key, console requests a real
	// platform JWT for the project owner.
	router            http.Handler
	platformJWT       string
	projectID         string
	slug              string
	schema            string
	dedDB             string
	secretKey         string
	publicKey         string
	scenario          string
	upgraded          bool
	sharedFingerprint string
	ownerUser         string
	s3                *storage.S3Client
	cipher            *dbprovider.Cipher
	keyB64            string
	funcSecret        []byte // FUNC_PASSWORD_SECRET the runner derives tenant logins from
	runnerHMAC        []byte // FUNCTIONS_RUNNER_HMAC_SECRET (gateway ↔ runner)
	runnerOn          bool   // the real Deno runner is up for this scenario
	bucket            string
	ownerEmail        string
	gw, dev           *pgxpool.Pool // shared-cluster runtime + developer pools
	rt                *pgxpool.Pool // the tenant DB's runtime role (RLS applies)
	shared            *pgxpool.Pool
	ded               *pgxpool.Pool // admin view of the dedicated DB, for assertions
}

func (e *teamEnv) dedCount(t *testing.T, table, where string) int {
	t.Helper()
	var n int
	q := fmt.Sprintf("SELECT count(*) FROM %s WHERE %s", pgx.Identifier{e.schema, table}.Sanitize(), where)
	if err := e.ded.QueryRow(context.Background(), q).Scan(&n); err != nil {
		t.Logf("dedCount: %v", err)
		return -1
	}
	return n
}

func (e *teamEnv) dedScalar(t *testing.T, format, table string) string {
	t.Helper()
	var v string
	q := fmt.Sprintf(format, pgx.Identifier{e.schema, table}.Sanitize())
	if err := e.ded.QueryRow(context.Background(), q).Scan(&v); err != nil {
		t.Fatalf("dedScalar: %v", err)
	}
	return v
}

// upload posts a small multipart file: with apiKey (+ end-user JWT) as the
// SDK, else as the console owner (platform JWT).
func (e *teamEnv) upload(apiKey, endUserJWT, url, key string) *httpResult {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("key", key)
	fw, _ := mw.CreateFormFile("file", "e2e.txt")
	_, _ = fw.Write([]byte("hello team"))
	_ = mw.Close()
	req := httptest.NewRequest("POST", url, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if apiKey != "" {
		req.Header.Set("apikey", apiKey)
		req.Header.Set("Authorization", "Bearer "+endUserJWT)
	} else {
		req.Header.Set("Authorization", "Bearer "+e.platformJWT)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return &httpResult{req: "POST " + url, code: rec.Code, body: rec.Body.String()}
}

// addAPIKeys creates an API key pair for a project and returns the secret key.
func (e *teamEnv) addAPIKeys(t *testing.T, projectID string) string {
	t.Helper()
	ctx := context.Background()
	pub, sec, pubHash, secHash, err := tenant.GenerateAPIKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := e.shared.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := tenant.StoreAPIKeys(ctx, tx, projectID, pubHash, pub[:14], secHash, sec[:14]); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return sec
}

// consoleFor issues a console request as the owner for another project.
func (e *teamEnv) consoleFor(projectID, method, path, body string) *httpResult {
	req := httptest.NewRequest(method, "http://api.eurobase.test/platform/projects/"+projectID+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.platformJWT)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return &httpResult{req: "console " + method + " " + path, code: rec.Code, body: rec.Body.String()}
}

// addProvisioningTeamProject creates a Team project owned by the same user
// whose project_databases row is still provisioning (no host), as between
// project creation and the provision worker's MarkActive.
type runnerRouteRow struct {
	id, host, db, state string
	port                int
}

// runnerRoute calls runner_get_tenant_db as the runner's own role
// (eurobase_function_runner), as functions-runner/server.ts does.
func (e *teamEnv) runnerRoute(ctx context.Context, projectID string) ([]runnerRouteRow, error) {
	tx, err := e.shared.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE eurobase_function_runner`); err != nil {
		return nil, err
	}
	r, err := tx.Query(ctx, `SELECT coalesce(id::text, ''), coalesce(host, ''), coalesce(port, 0), coalesce(database_name, ''), state
		FROM public.runner_get_tenant_db($1::uuid)`, projectID)
	if err != nil {
		return nil, fmt.Errorf("runner_get_tenant_db as eurobase_function_runner: %w", err)
	}
	defer r.Close()
	var out []runnerRouteRow
	for r.Next() {
		var row runnerRouteRow
		if err := r.Scan(&row.id, &row.host, &row.port, &row.db, &row.state); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, r.Err()
}

// ensureFuncLogins runs the worker's tenant-login pass with env.funcSecret
// for this project (shared: EnsureAll; Team: EnsureTeam with the provider
// CONNECT stand-in), as the runner and cron need it.
func (e *teamEnv) ensureFuncLogins(ctx context.Context) error {
	ensurer, err := tenantlogin.NewEnsurer(e.dev, "eurobase", e.funcSecret)
	if err != nil {
		return err
	}
	if e.scenario == "free" {
		if _, err := ensurer.EnsureAll(ctx); err != nil {
			return fmt.Errorf("EnsureAll: %w", err)
		}
		return nil
	}
	repo := dbprovider.NewRepo(e.shared)
	ensurer.PrepareDedicated = func(ctx context.Context, pool *pgxpool.Pool, schema string) error {
		ready, err := dbprovider.FuncRoleReady(ctx, pool, schema)
		if err != nil || ready {
			return err
		}
		return dbprovider.EnsureFuncRole(ctx, pool, schema)
	}
	ensurer.GrantDedicatedConnect = func(ctx context.Context, projectID, schema string, pool *pgxpool.Pool) error {
		if _, err := e.ded.Exec(ctx, fmt.Sprintf(`GRANT CONNECT ON DATABASE %s TO %s`,
			pgx.Identifier{e.dedDB}.Sanitize(), pgx.Identifier{tenantlogin.FuncRole(schema)}.Sanitize())); err != nil {
			return err
		}
		return dbprovider.EnsureFuncRole(ctx, pool, schema)
	}
	open := func(ctx context.Context, projectID string) (*pgxpool.Pool, tenantlogin.DedicatedDB, error) {
		if projectID != e.projectID {
			return nil, tenantlogin.DedicatedDB{}, nil // other scenarios' projects (their own cipher)
		}
		rec, err := repo.GetLiveByProject(ctx, projectID)
		if err != nil {
			return nil, tenantlogin.DedicatedDB{}, err
		}
		p, err := dbprovider.OpenOwnerPoolFor(ctx, rec, e.cipher)
		return p, tenantlogin.DedicatedDB{ID: rec.ID, Database: rec.DatabaseName}, err
	}
	if n, err := ensurer.EnsureTeam(ctx, open); err != nil || n != 1 {
		return fmt.Errorf("EnsureTeam: ensured %d, err %v", n, err)
	}
	return nil
}

func (e *teamEnv) addProvisioningTeamProject(t *testing.T) string {
	t.Helper()
	id := "7e4a0000-0000-4000-a000-" + randHexT(t, 6)
	slug := "stuck-" + randHexT(t, 4)
	mustExecT(t, e.shared, `INSERT INTO projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		VALUES ($1, $2, 'stuck', $3, $4, $5, 'fr-par', 'team', 'active')`,
		id, e.ownerUser, slug, "tenant_"+strings.ReplaceAll(id, "-", "_"), "b-"+slug)
	mustExecT(t, e.shared, `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'owner') ON CONFLICT DO NOTHING`, id, e.ownerUser)
	if _, err := dbprovider.NewRepo(e.shared).InsertProvisioning(context.Background(), id, &dbprovider.Instance{
		ProviderID: "e2e-" + slug, DBName: "rdb", Username: "eurobase_owner", State: dbprovider.StateProvisioning, Region: "fr-par",
	}, "scaleway", []byte("x"), []byte("x"), 1); err != nil {
		t.Fatalf("InsertProvisioning: %v", err)
	}
	return id
}

func (e *teamEnv) dedTableExists(t *testing.T, table string) bool {
	t.Helper()
	var ok bool
	_ = e.ded.QueryRow(context.Background(), `SELECT to_regclass($1) IS NOT NULL`, e.schema+"."+table).Scan(&ok)
	return ok
}

func randHexT(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func withDB(t *testing.T, raw, db string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + db
	return u
}

func setupTeamProject(t *testing.T, cfg teamTestConfig, scenario, dedDB string, upgraded bool) *teamEnv {
	t.Helper()
	ctx := context.Background()
	mustPool := func(u string) *pgxpool.Pool {
		p, err := pgxpool.New(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	admin, gw, dev := mustPool(cfg.sharedAdmin), mustPool(cfg.sharedGW), mustPool(cfg.sharedDev)
	free := scenario == "free"
	dedAdmin := admin // free: the tenant data lives on the shared cluster
	if !free {
		dedAdmin = mustPool(withDB(t, cfg.dedAdmin, dedDB).String())
	}

	ownerUser := "7e4a0000-0000-4000-9000-" + randHexT(t, 6)
	ownerEmail := "owner-" + ownerUser + "@team.test"
	projectID := "7e4a0000-0000-4000-8000-" + randHexT(t, 6)
	slug := "team-" + randHexT(t, 4)
	schema := "tenant_" + strings.ReplaceAll(projectID, "-", "_")
	mustExecT(t, admin, `INSERT INTO platform_users (id, email) VALUES ($1, $2)`, ownerUser, ownerEmail)

	var sharedFP string
	if free {
		mustExecT(t, admin, `INSERT INTO projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
			VALUES ($1, $2, 'team e2e', $3, $4, $5, 'fr-par', 'free', 'active')`, projectID, ownerUser, slug, schema, "b-"+slug)
		mustExecT(t, admin, `SELECT provision_tenant($1, 'team e2e', 'free')`, projectID)
	} else if upgraded {
		// As a Pro project: provision_tenant builds the shared schema (the
		// copy an upgrade leaves behind), then a decoy row goes in.
		mustExecT(t, admin, `INSERT INTO projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
			VALUES ($1, $2, 'team e2e', $3, $4, $5, 'fr-par', 'pro', 'active')`, projectID, ownerUser, slug, schema, "b-"+slug)
		mustExecT(t, admin, `SELECT provision_tenant($1, 'team e2e', 'pro')`, projectID)
		mustExecT(t, admin, fmt.Sprintf(`INSERT INTO %s (title) VALUES ($1)`, pgx.Identifier{schema, "todos"}.Sanitize()), decoyTitle)
		mustExecT(t, admin, `UPDATE projects SET plan = 'team' WHERE id = $1`, projectID)
		q := fmt.Sprintf(sharedFingerprintSQL, pgx.Identifier{schema, "todos"}.Sanitize(), pgx.Identifier{schema, "vault_secrets"}.Sanitize(), pgx.Identifier{schema, "storage_objects"}.Sanitize())
		if err := admin.QueryRow(ctx, q, schema).Scan(&sharedFP); err != nil {
			t.Fatal(err)
		}
	} else {
		mustExecT(t, admin, `INSERT INTO projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
			VALUES ($1, $2, 'team e2e', $3, $4, $5, 'fr-par', 'team', 'active')`, projectID, ownerUser, slug, schema, "b-"+slug)
	}
	mustExecT(t, admin, `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'owner')
		ON CONFLICT DO NOTHING`, projectID, ownerUser)

	// API keys (the SDK authenticates with the secret key).
	pub, sec, pubHash, secHash, err := tenant.GenerateAPIKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := tenant.StoreAPIKeys(ctx, tx, projectID, pubHash, pub[:14], secHash, sec[:14]); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// Connection cipher (= VAULT_ENCRYPTION_KEY) for the vault and project_databases.
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	keyB64 := base64.StdEncoding.EncodeToString(key)
	// Credentials start sealed with the legacy version, as rows written
	// before the domain-separated cipher are; credential_reseal moves them
	// (and every later check then runs on re-sealed rows).
	cipher, err := dbprovider.NewCipher(keyB64, dbprovider.CipherVersionLegacy)
	if err != nil {
		t.Fatal(err)
	}
	if free {
		// Marker objects only the tenant's own DB has, created as
		// eurobase_migrator like console-made tables on the shared cluster.
		tx, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range []string{
			`SET LOCAL ROLE eurobase_migrator`,
			fmt.Sprintf(`CREATE TABLE %s (id int)`, pgx.Identifier{schema, "ded_only"}.Sanitize()),
			fmt.Sprintf(`CREATE INDEX ded_only_marker_idx ON %s (id)`, pgx.Identifier{schema, "ded_only"}.Sanitize()),
			fmt.Sprintf(`CREATE POLICY ded_only_policy ON %s FOR SELECT USING (true)`, pgx.Identifier{schema, "ded_only"}.Sanitize()),
			fmt.Sprintf(`CREATE FUNCTION %s() RETURNS int LANGUAGE sql AS 'SELECT 1'`, pgx.Identifier{schema, "ded_only_fn"}.Sanitize()),
		} {
			if _, err := tx.Exec(ctx, q); err != nil {
				t.Fatalf("%v\n%s", err, q)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	} else {
		// The dedicated instance, bootstrapped as ProvisionTeamDatabaseWorker
		// does: owner DSN via BuildOwnerDSN (sslmode=require), display name =
		// project ID, then the provider's SetPrivilege (stand-in: CONNECT
		// granted by the instance superuser — the database is not the owner's
		// and PUBLIC has no CONNECT, as on Scaleway), then the readonly lockdown.
		ou := withDB(t, cfg.dedOwner, dedDB)
		ownerPW, _ := ou.User.Password()
		port, _ := strconv.Atoi(ou.Port())
		ownerDSN := dbprovider.BuildOwnerDSN(ou.User.Username(), ownerPW, ou.Hostname(), port, dedDB)
		creds, gotSchema, err := dbprovider.BootstrapDedicated(ctx, ownerDSN, projectID, projectID, cfg.runtimePW, cfg.readonlyPW, nil)
		if err != nil {
			t.Fatalf("BootstrapDedicated: %v", err)
		}
		if gotSchema != schema {
			t.Fatalf("bootstrap schema %q, projects.schema_name %q", gotSchema, schema)
		}
		for _, role := range []string{creds.Runtime.Username, creds.Readonly.Username} {
			mustExecT(t, dedAdmin, fmt.Sprintf(`GRANT CONNECT ON DATABASE %s TO %s`,
				pgx.Identifier{dedDB}.Sanitize(), pgx.Identifier{role}.Sanitize()))
		}
		if err := dbprovider.LockdownReadonlyGrants(ctx, ownerDSN, schema, nil); err != nil {
			t.Fatalf("LockdownReadonlyGrants: %v", err)
		}
		// Exists only here, so reads from the stale shared copy are detectable.
		// Created as the owner, like any customer table there.
		ownerPool := mustPool(ownerDSN)
		mustExecT(t, ownerPool, fmt.Sprintf(`CREATE TABLE %s (id int)`, pgx.Identifier{schema, "ded_only"}.Sanitize()))
		mustExecT(t, ownerPool, fmt.Sprintf(`CREATE INDEX ded_only_marker_idx ON %s (id)`, pgx.Identifier{schema, "ded_only"}.Sanitize()))
		mustExecT(t, ownerPool, fmt.Sprintf(`CREATE POLICY ded_only_policy ON %s FOR SELECT USING (true)`, pgx.Identifier{schema, "ded_only"}.Sanitize()))
		mustExecT(t, ownerPool, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS int LANGUAGE sql AS 'SELECT 1'`, pgx.Identifier{schema, "ded_only_fn"}.Sanitize()))

		// project_databases row with sealed credentials (owner, runtime, readonly).
		seal := func(s string) ([]byte, []byte, int16) {
			ct, nonce, ver, err := cipher.Seal(s)
			if err != nil {
				t.Fatal(err)
			}
			return ct, nonce, ver
		}
		repo := dbprovider.NewRepo(admin)
		ct, nonce, ver := seal(ownerPW)
		rec, err := repo.InsertProvisioning(ctx, projectID, &dbprovider.Instance{
			ProviderID: "e2e-" + slug, Host: ou.Hostname(), Port: port, DBName: dedDB,
			Username: ou.User.Username(), State: dbprovider.StateActive, Region: "fr-par",
		}, "scaleway", ct, nonce, ver)
		if err != nil {
			t.Fatalf("InsertProvisioning: %v", err)
		}
		ct, nonce, ver = seal(creds.Runtime.Password)
		if _, err := repo.SetRuntimeCredentials(ctx, rec.ID, creds.Runtime.Username, ct, nonce, ver); err != nil {
			t.Fatalf("SetRuntimeCredentials: %v", err)
		}
		ct, nonce, ver = seal(creds.Readonly.Password)
		if _, err := repo.SetReadonlyCredentials(ctx, rec.ID, creds.Readonly.Username, ct, nonce, ver); err != nil {
			t.Fatalf("SetReadonlyCredentials: %v", err)
		}
	}

	// The tenant DB's runtime role, for RLS-level checks: the gateway role
	// on the shared cluster, or eurobase_gateway on the dedicated instance.
	rt := gw
	if !free {
		ru := withDB(t, cfg.dedOwner, dedDB)
		ru.User = url.UserPassword("eurobase_gateway", cfg.runtimePW)
		q := ru.Query()
		q.Set("sslmode", "require")
		ru.RawQuery = q.Encode()
		rt = mustPool(ru.String())
	}

	// The real router with Team routing on and platform auth wired as in
	// cmd/gateway.
	t.Setenv("VAULT_ENCRYPTION_KEY", keyB64)
	t.Setenv("TEAM_TIER_ROUTING", "1")
	platformSvc := auth.NewPlatformAuthService(gw, randHexT(t, 32))
	jwt, _, err := platformSvc.IssuePlatformJWT(ownerUser, ownerEmail, false)
	if err != nil {
		t.Fatal(err)
	}
	subdomain := auth.NewSubdomainMiddleware(gw, "eurobase.test")
	vaultSvc, err := vault.NewVaultService(gw, keyB64)
	if err != nil {
		t.Fatal(err)
	}
	s3Client, err := storage.NewS3Client(cfg.s3Endpoint, "fr-par", cfg.s3Key, cfg.s3Secret)
	if err != nil {
		t.Fatal(err)
	}
	// Garage (the harness's S3) doesn't implement PutBucketAcl, which
	// CreateBucket issues after creating the bucket (501 NotImplemented);
	// its buckets are private anyway. Accept only that, and check the
	// bucket exists. Garage has no object lock or public ACLs either —
	// Legal-Team WORM checks can't run against it.
	if err := s3Client.CreateBucket(ctx, "eurobase-"+slug); err != nil {
		var apiErr interface{ ErrorCode() string }
		if !strings.Contains(err.Error(), "PutBucketAcl") || !errors.As(err, &apiErr) || apiErr.ErrorCode() != "NotImplemented" {
			t.Fatalf("create bucket: %v", err)
		}
	}
	if ok, err := s3Client.BucketExists(ctx, "eurobase-"+slug); err != nil || !ok {
		t.Fatalf("bucket eurobase-%s missing after create: %v", slug, err)
	}
	// The real functions runner (#676): the Deno server from
	// functions-runner/, started per scenario (its VAULT_ENCRYPTION_KEY is
	// the scenario's), wired to this router both ways — invocations go
	// gateway → runner, ctx.storage calls come back runner → gateway.
	funcSecret, runnerHMAC := []byte(randHexT(t, 32)), []byte(randHexT(t, 32))
	runnerURL, runnerOn := "", false
	var fnSigner *functions.Signer
	var runnerPort int
	if cfg.deno != "" {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		runnerPort = ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		runnerURL = fmt.Sprintf("http://127.0.0.1:%d", runnerPort)
		if fnSigner, err = functions.NewSigner(string(runnerHMAC)); err != nil {
			t.Fatal(err)
		}
	} else if os.Getenv("TEAM_TEST_REQUIRE_RUNNER") == "1" {
		t.Fatal("TEAM_TEST_REQUIRE_RUNNER=1 but no deno (TEAM_TEST_DENO)")
	}
	// The console's cron Test Run (#645) dry-runs as the tenant login.
	t.Setenv("FUNC_PASSWORD_SECRET", string(funcSecret))
	router := NewRouter(gw, dev, nil, auth.NewPlatformAuthMiddleware(platformSvc), platformSvc, nil, nil, s3Client, nil, nil,
		subdomain, nil, nil, plans.NewLimitsService(gw), vaultSvc, runnerURL, fnSigner, string(runnerHMAC), nil, nil, nil, nil, SSOWiring{}, nil, nil)
	if cfg.deno != "" {
		gatewaySrv := httptest.NewServer(router)
		t.Cleanup(gatewaySrv.Close)
		startRunner(t, cfg, runnerPort, map[string]string{
			"DATABASE_URL_FUNCTION_RUNNER":         cfg.sharedRunner,
			"FUNC_PASSWORD_SECRET":                 string(funcSecret),
			"VAULT_ENCRYPTION_KEY":                 keyB64,
			"FUNCTIONS_RUNNER_HMAC_SECRET":         string(runnerHMAC),
			"FUNCTIONS_RUNNER_HMAC_REQUIRE_SIGNED": "true",
			"GATEWAY_INTERNAL_URL":                 gatewaySrv.URL,
			// Not inherited from the developer's shell: direct tenant
			// connections, default cap.
			"FUNC_DB_POOLER_URL":     "",
			"RUNNER_TENANT_CONN_CAP": "",
			"DATABASE_URL":           "",
		})
		runnerOn = true
	}

	return &teamEnv{
		router: router, platformJWT: jwt, projectID: projectID, slug: slug, schema: schema, dedDB: dedDB,
		secretKey: sec, publicKey: pub, s3: s3Client, bucket: "eurobase-" + slug, cipher: cipher, keyB64: keyB64, scenario: scenario, upgraded: upgraded, sharedFingerprint: sharedFP, shared: admin,
		ownerUser: ownerUser, ownerEmail: ownerEmail, gw: gw, dev: dev, rt: rt, ded: dedAdmin,
		funcSecret: funcSecret, runnerHMAC: runnerHMAC, runnerOn: runnerOn,
	}
}

// startRunner runs functions-runner/server.ts (the production entrypoint)
// on port and waits until it listens.
func startRunner(t *testing.T, cfg teamTestConfig, port int, env map[string]string) {
	t.Helper()
	dir, err := filepath.Abs("../../functions-runner")
	if err != nil {
		t.Fatal(err)
	}
	// Like production (deploy/docker/Dockerfile.fn), certificate checks
	// are off — here only for the local dedicated instance's self-signed
	// certificate. When production trusts Scaleway's CA instead (stage B),
	// dedicated instances must be covered too.
	cmd := exec.Command(cfg.deno, "run", "--allow-net", "--allow-env", "--allow-read="+dir,
		"--unstable-worker-options", "--unsafely-ignore-certificate-errors=localhost,127.0.0.1", "--no-lock",
		filepath.Join(dir, "server.ts"))
	cmd.Env = append(os.Environ(), "PORT="+strconv.Itoa(port))
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start runner: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		if t.Failed() {
			t.Logf("runner output:\n%s", out.String())
		}
	})
	deadline := time.Now().Add(60 * time.Second) // first run downloads remote imports
	for {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			c.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("runner didn't listen on %d within 60s:\n%s", port, out.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func mustExecT(t *testing.T, p *pgxpool.Pool, q string, args ...any) {
	t.Helper()
	if _, err := p.Exec(context.Background(), q, args...); err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
}
