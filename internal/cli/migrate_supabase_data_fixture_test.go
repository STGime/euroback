package cli

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/query"
)

// TestDataImport_Fixture runs the whole data path against
// ASSESS_SCRATCH_DB_URL (a throwaway local database, never a real
// project): a Supabase-shaped source with the column types, identity
// columns, sequences, triggers and kinds of user that break a copy →
// the migration files → the server's validator → an apply into a
// project-like schema → a row-for-row comparison. Everything happens in
// one transaction that is rolled back.
func TestDataImport_Fixture(t *testing.T) {
	dsn := os.Getenv("ASSESS_SCRATCH_DB_URL")
	if dsn == "" {
		t.Skip("ASSESS_SCRATCH_DB_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn := openSupabaseFixture(ctx, t, dsn)
	defer conn.Close(ctx)

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Source: an empty public schema plus Supabase's auth columns.
	if _, err := tx.Exec(ctx, dataFixtureAuthSQL); err != nil {
		t.Fatalf("auth fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL search_path TO public;"+strings.ReplaceAll(dataFixtureTablesSQL, "%USERS%", "auth.users")+dataFixtureRowsSQL); err != nil {
		t.Fatalf("source tables: %v", err)
	}

	dir := t.TempDir()
	now := time.Now()
	writer := newSQLFileWriter(dir, "data", now, maxPartBytes)
	nUsers, nLinks, authNotes, err := emitAuthData(ctx, tx, writer, now)
	if err != nil {
		t.Fatalf("users: %v", err)
	}
	counts := map[string]int64{}
	nTables, rowNotes, err := emitRowData(ctx, tx, writer, "", counts)
	if err != nil {
		t.Fatalf("rows: %v", err)
	}
	paths, err := writer.Close()
	if err != nil {
		t.Fatal(err)
	}
	// 5 of 7: not the deleted user, not the SSO user. 2 links: not the
	// "email" identity, not the deleted user's.
	if nUsers != 5 || nLinks != 2 || nTables != 4 {
		t.Errorf("users=%d links=%d tables=%d, want 5 / 2 / 4", nUsers, nLinks, nTables)
	}
	notes := strings.Join(append(authNotes, rowNotes...), "\n")
	for _, want := range []string{"1 user(s) deleted", "1 SSO user(s)", "neither email nor phone", "banned until a date", "triggers are switched off while rows are inserted into: orders"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q:\n%s", want, notes)
		}
	}

	// Target: what a project schema has, plus the same tables.
	if _, err := tx.Exec(ctx, `
		CREATE SCHEMA tenant_dataimport;
		SET LOCAL search_path TO tenant_dataimport;
		CREATE TABLE users (
			id uuid PRIMARY KEY, email text UNIQUE, phone text, password_hash text, display_name text, avatar_url text,
			metadata jsonb DEFAULT '{}', provider text DEFAULT 'email', provider_user_id text,
			email_confirmed_at timestamptz, phone_confirmed_at timestamptz, last_sign_in_at timestamptz,
			banned_at timestamptz, created_at timestamptz DEFAULT now(), updated_at timestamptz DEFAULT now());
		CREATE TABLE user_identities (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(), user_id uuid NOT NULL REFERENCES users(id),
			provider text NOT NULL, provider_user_id text NOT NULL, identity_data jsonb DEFAULT '{}',
			last_sign_in_at timestamptz, created_at timestamptz DEFAULT now(), updated_at timestamptz DEFAULT now(),
			UNIQUE (provider, provider_user_id));
	`+strings.ReplaceAll(dataFixtureTablesSQL, "%USERS%", "users")); err != nil {
		t.Fatalf("target schema: %v", err)
	}
	for _, p := range paths {
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := query.ValidateTenantMigrationSQL(string(body)); err != nil {
			t.Fatalf("the server would refuse %s: %v", p, err)
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v\n%s", p, err, body)
		}
	}

	// Every table arrives row for row — including the audit rows (the
	// insert trigger didn't write a second set) and updated_at (the
	// touch trigger didn't overwrite it).
	for _, table := range []string{"categories", "kitchen_sink", "orders", "audit_log"} {
		var src, dst string
		q := `SELECT count(*) || ' rows ' || md5(coalesce(string_agg(x::text, E'\n' ORDER BY x::text), '')) FROM `
		if err := tx.QueryRow(ctx, q+"public."+table+" x").Scan(&src); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRow(ctx, q+"tenant_dataimport."+table+" x").Scan(&dst); err != nil {
			t.Fatal(err)
		}
		if src != dst || strings.HasPrefix(src, "0 rows") {
			t.Errorf("%s differs: source %s, imported %s", table, src, dst)
		}
	}
	// Sequences stand where they stood, so new rows get new ids.
	var seqDiff int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM pg_sequences s
		  LEFT JOIN pg_sequences d ON d.schemaname = 'tenant_dataimport' AND d.sequencename = s.sequencename
		 WHERE s.schemaname = 'public' AND s.last_value IS DISTINCT FROM d.last_value`).Scan(&seqDiff); err != nil {
		t.Fatal(err)
	}
	if seqDiff != 0 {
		t.Errorf("%d sequence(s) not at the source's position", seqDiff)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO categories (name) VALUES ('new'); INSERT INTO kitchen_sink (t) VALUES ('new'); SELECT nextval('ticket_no')`); err != nil {
		t.Errorf("insert after the import: %v", err)
	}
	// Triggers are back on; the one that was off on the source stays off.
	var states string
	if err := tx.QueryRow(ctx, `
		SELECT string_agg(tgname || '=' || tgenabled::text, ',' ORDER BY tgname) FROM pg_trigger
		 WHERE tgrelid = 'tenant_dataimport.orders'::regclass AND NOT tgisinternal`).Scan(&states); err != nil {
		t.Fatal(err)
	}
	if states != "orders_audit=O,orders_off=D,orders_touch=O" {
		t.Errorf("trigger states after the import: %s", states)
	}

	// Users.
	var users string
	if err := tx.QueryRow(ctx, `
		SELECT string_agg(concat_ws('|', left(id::text, 1), coalesce(email, '-'), coalesce(phone, '-'),
		         CASE WHEN password_hash IS NULL THEN 'nopw' ELSE 'pw' END,
		         CASE WHEN banned_at IS NULL THEN 'ok' ELSE 'banned' END,
		         CASE WHEN email_confirmed_at IS NULL THEN 'unconfirmed' ELSE 'confirmed' END,
		         coalesce(display_name, '-')), E'\n' ORDER BY id)
		  FROM users`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"1|alice@example.org|-|pw|ok|confirmed|Alice O'Brien",
		"2|bob@example.org|-|nopw|ok|confirmed|Bob",
		"3|-|+4915112345678|nopw|banned|unconfirmed|-",
		"4|expired-ban@example.org|-|pw|ok|unconfirmed|-",
		"5|-|-|nopw|ok|unconfirmed|-",
	}, "\n")
	if users != want {
		t.Errorf("users:\n%s\nwant:\n%s", users, want)
	}
	var links string
	if err := tx.QueryRow(ctx, `SELECT string_agg(provider || ':' || provider_user_id, ',' ORDER BY provider) FROM user_identities`).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != "github:gh-42,google:google-sub-123" {
		t.Errorf("OAuth links: %s", links)
	}
}

// dataFixtureAuthSQL empties the public schema and gives auth.users /
// auth.identities the columns a Supabase project has, with one user of
// each kind the import treats differently.
const dataFixtureAuthSQL = `
DROP SCHEMA public CASCADE;
CREATE SCHEMA public;
ALTER TABLE auth.users
  ADD COLUMN encrypted_password text, ADD COLUMN email_confirmed_at timestamptz,
  ADD COLUMN last_sign_in_at timestamptz, ADD COLUMN raw_app_meta_data jsonb,
  ADD COLUMN raw_user_meta_data jsonb, ADD COLUMN created_at timestamptz,
  ADD COLUMN updated_at timestamptz, ADD COLUMN phone text,
  ADD COLUMN phone_confirmed_at timestamptz, ADD COLUMN banned_until timestamptz,
  ADD COLUMN deleted_at timestamptz, ADD COLUMN is_anonymous boolean NOT NULL DEFAULT false,
  ADD COLUMN is_sso_user boolean NOT NULL DEFAULT false;
ALTER TABLE auth.identities
  ADD COLUMN provider_id text, ADD COLUMN user_id uuid, ADD COLUMN identity_data jsonb,
  ADD COLUMN last_sign_in_at timestamptz, ADD COLUMN created_at timestamptz, ADD COLUMN updated_at timestamptz;
DELETE FROM auth.users;
INSERT INTO auth.users (id, email, encrypted_password, email_confirmed_at, raw_app_meta_data, raw_user_meta_data, created_at, phone, banned_until, deleted_at, is_anonymous, is_sso_user) VALUES
 ('11111111-1111-1111-1111-111111111111', 'Alice@Example.org', '$2a$10$7EqJtq98hPqEX7fNZaFWoOhi5BWX4Z1Z3MZ5uG1h1uG1h1uG1h1uG', now(), '{"provider":"email"}', '{"display_name":"Alice O''Brien"}', '2025-01-01+00', NULL, NULL, NULL, false, false),
 ('22222222-2222-2222-2222-222222222222', 'bob@example.org', NULL, now(), '{"provider":"google"}', '{"full_name":"Bob"}', '2025-02-01+00', NULL, NULL, NULL, false, false),
 ('33333333-3333-3333-3333-333333333333', NULL, NULL, NULL, '{}', '{}', '2025-03-01+00', '4915112345678', '2999-01-01+00', NULL, false, false),
 ('44444444-4444-4444-4444-444444444444', 'expired-ban@example.org', '$2a$10$7EqJtq98hPqEX7fNZaFWoOhi5BWX4Z1Z3MZ5uG1h1uG1h1uG1h1uG', NULL, '{}', '{}', '2025-04-01+00', NULL, '2020-01-01+00', NULL, false, false),
 ('55555555-5555-5555-5555-555555555555', NULL, NULL, NULL, '{}', '{}', '2025-05-01+00', NULL, NULL, NULL, true, false),
 ('66666666-6666-6666-6666-666666666666', 'gone@example.org', NULL, NULL, '{}', '{}', '2025-06-01+00', NULL, NULL, now(), false, false),
 ('77777777-7777-7777-7777-777777777777', 'alice@example.org', NULL, NULL, '{}', '{}', '2025-07-01+00', NULL, NULL, NULL, false, true);
INSERT INTO auth.identities (id, provider_id, user_id, identity_data, provider, created_at) VALUES
 (gen_random_uuid(), '11111111-1111-1111-1111-111111111111', '11111111-1111-1111-1111-111111111111', '{}', 'email', '2025-01-01+00'),
 (gen_random_uuid(), 'google-sub-123', '22222222-2222-2222-2222-222222222222', '{"sub":"google-sub-123"}', 'google', '2025-02-01+00'),
 (gen_random_uuid(), 'gh-42', '22222222-2222-2222-2222-222222222222', NULL, 'github', '2025-02-03+00'),
 (gen_random_uuid(), 'gh-99', '66666666-6666-6666-6666-666666666666', '{}', 'github', '2025-06-01+00');
`

// dataFixtureTablesSQL is run once as the source (public) and once as
// the target; %USERS% is auth.users or the project's users table.
const dataFixtureTablesSQL = `
CREATE TYPE mood AS ENUM ('ok', 'not ok');
CREATE DOMAIN pos_int AS integer CHECK (VALUE > 0);
CREATE TYPE addr AS (street text, zip text);
CREATE SEQUENCE ticket_no START 1000;
CREATE TABLE categories (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  parent_id bigint REFERENCES categories(id),
  name text NOT NULL);
CREATE TABLE kitchen_sink (
  id serial PRIMARY KEY, owner uuid REFERENCES %USERS%(id),
  t text, j jsonb, js json, n numeric(12,4), f double precision, r real, b bytea,
  ts timestamptz, tsn timestamp, d date, tm time, iv interval, ip inet,
  tags text[], nums int[], m mood, ms mood[], qty pos_int, a addr, u uuid, flag boolean,
  big bigint, vc varchar(20), ch char(3), bits bit(5), tsr tstzrange,
  total numeric GENERATED ALWAYS AS (n * 2) STORED);
CREATE TABLE audit_log (id bigserial PRIMARY KEY, what text);
CREATE TABLE orders (
  id serial PRIMARY KEY, user_id uuid NOT NULL REFERENCES %USERS%(id),
  category_id bigint REFERENCES categories(id), amount numeric(10,2), updated_at timestamptz);
CREATE FUNCTION log_order() RETURNS trigger LANGUAGE plpgsql AS $f$ BEGIN INSERT INTO audit_log (what) VALUES ('order ' || NEW.id); RETURN NEW; END $f$;
CREATE FUNCTION touch_updated() RETURNS trigger LANGUAGE plpgsql AS $f$ BEGIN NEW.updated_at = now(); RETURN NEW; END $f$;
CREATE TRIGGER orders_audit AFTER INSERT ON orders FOR EACH ROW EXECUTE FUNCTION log_order();
CREATE TRIGGER orders_touch BEFORE INSERT OR UPDATE ON orders FOR EACH ROW EXECUTE FUNCTION touch_updated();
CREATE TRIGGER orders_off AFTER INSERT ON orders FOR EACH ROW EXECUTE FUNCTION log_order();
ALTER TABLE orders DISABLE TRIGGER orders_off;
`

// dataFixtureRowsSQL fills the source only.
const dataFixtureRowsSQL = `
INSERT INTO categories (name) VALUES ('root'), ('later-root');
INSERT INTO categories (parent_id, name) VALUES (1, 'child');
UPDATE categories SET parent_id = 2 WHERE id = 1;  -- a row that refers to a later one
INSERT INTO kitchen_sink (owner, t, j, js, n, f, r, b, ts, tsn, d, tm, iv, ip, tags, nums, m, ms, qty, a, u, flag, big, vc, ch, bits, tsr) VALUES
 ('11111111-1111-1111-1111-111111111111', E'it''s a "test"\\ with\nnewline; DROP TABLE x; -- $$ $1 \t tab',
  '{"a":"it''s","n":[1,2,{"x":null}]}', '"scalar"', 1234.5678, 'NaN', 1.5, '\xdeadbeef00',
  '2025-01-01 12:00:00.123456+02', '2025-01-01 12:00:00', '2025-12-31', '23:59:59.5', '1 year 2 mons 3 days', '10.0.0.1/24',
  ARRAY['a', 'b c', 'd"e', 'f,g', NULL, ''], ARRAY[1, 2, 3], 'not ok', ARRAY['ok', 'not ok']::mood[], 5, ROW('Main St', '12345'),
  'a0000000-0000-0000-0000-000000000001', true, 9223372036854775807, 'vc', 'ab', B'10101', '[2025-01-01,2025-02-01)'),
 ('55555555-5555-5555-5555-555555555555', '', 'null', 'null', NULL, 'Infinity', '-Infinity', '', 'infinity', NULL, 'infinity',
  NULL, NULL, NULL, '{}', NULL, NULL, NULL, NULL, NULL, NULL, false, -1, NULL, NULL, NULL, NULL),
 (NULL, NULL, NULL, NULL, 0, 0.1, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL);
INSERT INTO orders (user_id, category_id, amount) VALUES
 ('11111111-1111-1111-1111-111111111111', 1, 9.99), ('22222222-2222-2222-2222-222222222222', 3, 100), ('55555555-5555-5555-5555-555555555555', NULL, 1);
DELETE FROM orders WHERE id = 2;  -- the sequence is ahead of max(id)
ALTER TABLE orders DISABLE TRIGGER USER;
UPDATE orders SET updated_at = '2024-01-01+00';
ALTER TABLE orders ENABLE TRIGGER USER;
ALTER TABLE orders DISABLE TRIGGER orders_off;
SELECT nextval('ticket_no'), nextval('ticket_no');
`
