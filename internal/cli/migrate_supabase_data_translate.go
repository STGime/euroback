package cli

// #270 (part of #267): Supabase → Eurobase data translator.
//
// Given rows read from a Supabase project — public tables and
// `auth.users` / `auth.identities` — produces SQL that runs cleanly
// against a Eurobase tenant schema. The translator lives here so its
// behavior is pinnable by unit tests without needing a live database.
//
// Emission strategy: multi-row `INSERT INTO t (cols) VALUES (row1),
// (row2), …` batched at ~1000 rows per statement. We don't use
// `COPY … FROM STDIN` because the tenant migrations endpoint (#190)
// accepts a SQL body over HTTP; there's no way to pipe stdin through
// it. Multi-row INSERT is ~5–10× slower than COPY but works with the
// existing infrastructure — a proper streaming path lands with the
// backend endpoint in a follow-up.
//
// Auth users get translated into Eurobase's `users` + `user_identities`
// table pair: `auth.users` has one row per human, `auth.identities`
// has one row per (user, provider). Eurobase mirrors this shape after
// migration `000032`.
//
// UUIDs are preserved across the migration so RLS policies keep
// working — a policy like `USING (user_id = auth_uid())` still lines
// up because the underlying UUIDs didn't change.

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// dataBatchSize is the number of rows we pack into one multi-row
// INSERT. Postgres's parser handles 10k+ per statement without issue,
// but 1000 keeps individual failed batches small enough that the
// tenant can eyeball the retry.
const dataBatchSize = 1000

// tableRef describes one Supabase public-schema table for the row
// migration. `columns` is the ordered list of columns to include (we
// intentionally drop system columns like `xmin`, and re-order to
// match the target table's expected column order at emit time —
// pg_dump-style).
type tableRef struct {
	name    string   // e.g. "orders"
	columns []string // ordered
}

// fkEdge is one foreign-key dependency between two public tables:
// `from` references `to`. Used by the topology sort so parents get
// INSERT'd before children.
type fkEdge struct {
	from string // child (has the FK)
	to   string // parent (referenced)
}

// sortTablesByFK returns the input tables in topological order —
// parents (referenced tables) before children (referencing tables).
// Returns an error listing the cycle members when a real FK cycle is
// present, because emitting a wrong order would silently apply-time-
// fail on FK violations. Self-references are tolerated (Postgres
// accepts them within a single-statement multi-row INSERT if the
// rows are consistent).
//
// Deterministic: given the same inputs, always returns the same
// order. Sort is stable within an FK level via lexicographic
// tiebreak. (#275 review H #7.)
func sortTablesByFK(tables []tableRef, edges []fkEdge) ([]tableRef, error) {
	byName := make(map[string]tableRef, len(tables))
	names := make([]string, 0, len(tables))
	for _, t := range tables {
		byName[t.name] = t
		names = append(names, t.name)
	}
	sort.Strings(names)

	// indeg = number of parents each node still waits for.
	indeg := make(map[string]int, len(tables))
	// deps["parent"] = list of children that reference "parent".
	deps := make(map[string][]string, len(tables))
	for _, e := range edges {
		if _, ok := byName[e.from]; !ok {
			continue
		}
		if _, ok := byName[e.to]; !ok {
			continue
		}
		if e.from == e.to {
			// Self-reference — doesn't participate in ordering.
			continue
		}
		indeg[e.from]++
		deps[e.to] = append(deps[e.to], e.from)
	}

	// Kahn's algorithm, ordering candidates lexicographically for
	// determinism.
	var out []tableRef
	remaining := len(names)
	for remaining > 0 {
		var ready []string
		for _, n := range names {
			if _, alreadyOut := indeg[n]; alreadyOut && indeg[n] < 0 {
				continue
			}
			if indeg[n] == 0 {
				ready = append(ready, n)
			}
		}
		if len(ready) == 0 {
			// A real FK cycle among two or more tables — the tenant
			// must resolve it themselves (either DEFERRABLE the
			// constraints on the target project, or migrate one side
			// with rows only after both have been imported). We fail
			// LOUD rather than emit a wrong order that silently blows
			// up at apply time.
			var stuck []string
			for _, n := range names {
				if indeg[n] < 0 {
					continue
				}
				stuck = append(stuck, n)
			}
			return nil, fmt.Errorf("FK cycle among tables %v — resolve on Supabase (DEFERRABLE or drop one side) and rerun", stuck)
		}
		sort.Strings(ready)
		for _, n := range ready {
			out = append(out, byName[n])
			indeg[n] = -1
			remaining--
			for _, child := range deps[n] {
				indeg[child]--
			}
		}
	}
	return out, nil
}

// supabaseUser is the projected shape we read from Supabase's
// `auth.users`. Only the fields we translate — Supabase has ~30
// columns, most are session state that Eurobase reissues on first
// sign-in post-cutover.
type supabaseUser struct {
	ID                string     // uuid
	Email             *string    // nullable
	EncryptedPassword *string    // bcrypt (may be NULL for OAuth-only users)
	EmailConfirmedAt  *time.Time // nullable
	Phone             *string    // nullable
	PhoneConfirmedAt  *time.Time // nullable
	LastSignInAt      *time.Time // nullable
	BannedUntil       *time.Time // Supabase's ban field, → Eurobase's banned_at
	RawUserMetaData   string     // JSONB (raw JSON text — no need to parse)
	RawAppMetaData    string     // JSONB (raw JSON text)
	CreatedAt         time.Time
	UpdatedAt         *time.Time
}

// supabaseIdentity is one row from `auth.identities` — the OAuth-link
// side table. One user can have many identities (a Google + a GitHub
// login attached to the same account).
type supabaseIdentity struct {
	ID           string     // uuid — Eurobase generates its own, we drop this
	UserID       string     // uuid — preserved so it lines up with users.id
	Provider     string     // "google" / "github" / …
	ProviderID   string     // opaque per-provider user identifier
	IdentityData string     // JSONB — includes profile fields Supabase cached
	LastSignInAt *time.Time // nullable
	CreatedAt    time.Time
	UpdatedAt    *time.Time
}

// eurobaseUserRow is what we emit into `users`. Exposed for tests.
// Field-name matches the SQL column list emittable directly.
type eurobaseUserRow struct {
	ID               string
	Email            *string
	Phone            *string
	PasswordHash     *string
	Metadata         string // JSONB
	EmailConfirmedAt *time.Time
	PhoneConfirmedAt *time.Time
	LastSignInAt     *time.Time
	BannedAt         *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// eurobaseIdentityRow is what we emit into `user_identities`. Exposed
// for tests.
type eurobaseIdentityRow struct {
	UserID         string
	Provider       string
	ProviderUserID string
	IdentityData   string
	LastSignInAt   *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// translateAuthUser maps one Supabase auth.users row to a Eurobase
// users row. Pure — no DB access. Returns the row and a "notes" list
// with any information the tenant might want to eyeball (e.g. "no
// password_hash — user must reset").
func translateAuthUser(u supabaseUser) (eurobaseUserRow, []string) {
	var notes []string

	// Password: preserved verbatim. Supabase and Eurobase both use
	// bcrypt with the same output shape ($2a$… / $2b$…), so
	// bcrypt.CompareHashAndPassword works across the boundary. NULL
	// on Supabase → NULL on Eurobase (OAuth-only user must sign in
	// via the provider first, which sets a fresh hash if the tenant
	// enables password fallback).
	pw := u.EncryptedPassword
	if pw == nil {
		notes = append(notes, "no password on source — user must sign in via OAuth to establish local password")
	}

	// Metadata: Supabase's `raw_user_meta_data` is where clients stash
	// display_name / avatar_url; `raw_app_meta_data` is where the
	// server (and app code) stashes provider / providers / role /
	// tenant-membership hints. Eurobase collapses both into a single
	// `metadata` JSONB. We merge them, putting user-meta at the top
	// level (existing app code reads it there) and nesting app-meta
	// under an `app_metadata` key so nothing is lost. (#275 review
	// H #6 — raw_app_meta_data was previously discarded silently.)
	metadata := mergeUserAndAppMetadata(u.RawUserMetaData, u.RawAppMetaData)

	// updated_at defaults to created_at when the source didn't record
	// one (Supabase started stamping it later in its schema history).
	updatedAt := u.CreatedAt
	if u.UpdatedAt != nil {
		updatedAt = *u.UpdatedAt
	}

	return eurobaseUserRow{
		ID:               u.ID,
		Email:            u.Email,
		Phone:            u.Phone,
		PasswordHash:     pw,
		Metadata:         metadata,
		EmailConfirmedAt: u.EmailConfirmedAt,
		PhoneConfirmedAt: u.PhoneConfirmedAt,
		LastSignInAt:     u.LastSignInAt,
		BannedAt:         u.BannedUntil, // Supabase's `banned_until` maps to Eurobase's soft-delete `banned_at`
		CreatedAt:        u.CreatedAt,
		UpdatedAt:        updatedAt,
	}, notes
}

// translateAuthIdentity maps one Supabase auth.identities row to a
// Eurobase user_identities row. Pure — no DB access.
func translateAuthIdentity(i supabaseIdentity) eurobaseIdentityRow {
	// Supabase's identity_data is the profile blob (name, email, avatar,
	// scopes) — Eurobase's identity_data has the same purpose.
	data := i.IdentityData
	if data == "" || data == "null" {
		data = "{}"
	}
	updatedAt := i.CreatedAt
	if i.UpdatedAt != nil {
		updatedAt = *i.UpdatedAt
	}
	return eurobaseIdentityRow{
		UserID:         i.UserID,
		Provider:       i.Provider,
		ProviderUserID: i.ProviderID,
		IdentityData:   data,
		LastSignInAt:   i.LastSignInAt,
		CreatedAt:      i.CreatedAt,
		UpdatedAt:      updatedAt,
	}
}

// sqlLiteral formats one column value as a Postgres SQL literal. `v`
// is a Go type as returned by pgx (string, int, time.Time, []byte,
// nil, etc.). This is used to build the multi-row INSERT VALUES tuples.
//
// SECURITY: the tenant supplies the source database, but the OUTPUT
// SQL file is intended to run against the tenant's own target project.
// Escapes are deliberate — a hostile row value ('; DROP TABLE …) must
// not become an executable statement on the target. All strings are
// wrapped in single quotes with `'` doubled per SQL standard, and
// `standard_conforming_strings=on` (the modern default) is assumed
// so backslashes aren't interpreted.
func sqlLiteral(v interface{}) string {
	if v == nil {
		return "NULL"
	}
	switch t := v.(type) {
	case jsonbValue:
		// JSONB literal with explicit cast so Postgres parses at
		// insert time. Empty / literal-null gets normalised to '{}'.
		s := string(t)
		if s == "" || s == "null" {
			s = "{}"
		}
		return "'" + strings.ReplaceAll(s, "'", "''") + "'::jsonb"
	case typedLiteral:
		// Typed literal — the CLI casts numeric/interval/inet/etc.
		// columns to text in the SELECT so pgx returns them as
		// strings; we re-emit as `'value'::type` so Postgres re-
		// parses. Without this, sqlLiteral's default fallback would
		// `%v`-print a pgtype wrapper into a Go-struct dump. (#275
		// review ship-blocker #3/#4.)
		return "'" + strings.ReplaceAll(t.value, "'", "''") + "'::" + t.pgType
	case string:
		return "'" + strings.ReplaceAll(t, "'", "''") + "'"
	case *string:
		if t == nil {
			return "NULL"
		}
		return "'" + strings.ReplaceAll(*t, "'", "''") + "'"
	case bool:
		if t {
			return "true"
		}
		return "false"
	case int:
		return fmt.Sprintf("%d", t)
	case int32:
		return fmt.Sprintf("%d", t)
	case int64:
		return fmt.Sprintf("%d", t)
	case float32:
		return fmt.Sprintf("%g", t)
	case float64:
		return fmt.Sprintf("%g", t)
	case time.Time:
		// timestamptz literal — Postgres accepts RFC3339 with a suffix
		// cast to be explicit about the type.
		return "'" + t.UTC().Format(time.RFC3339Nano) + "'::timestamptz"
	case *time.Time:
		if t == nil {
			return "NULL"
		}
		return "'" + t.UTC().Format(time.RFC3339Nano) + "'::timestamptz"
	case []byte:
		// bytea literal — hex-escaped. Portable and doesn't require
		// bytea_output to be `hex`.
		return "'\\x" + hex.EncodeToString(t) + "'::bytea"
	default:
		// Fallback: format as string. Callers should route known
		// pgx types through the branches above; this catches
		// anything unusual (custom types, arrays we don't parse).
		s := fmt.Sprintf("%v", t)
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
}

// quoteIdent wraps an identifier in double quotes if it's not a
// bare identifier (or if it collides with a Postgres reserved word).
// For MVP we quote all identifiers unconditionally — safer, and
// pg_dump does the same.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// emitInsertBatch writes one multi-row INSERT statement for `rows` into
// `dest`. Column order matches `columns`; each row must have exactly
// `len(columns)` values. Returns the byte count written for progress.
//
// Emits nothing if `rows` is empty (Postgres rejects a zero-row VALUES
// clause).
func emitInsertBatch(dest *strings.Builder, table string, columns []string, rows [][]interface{}) {
	if len(rows) == 0 {
		return
	}
	dest.WriteString("INSERT INTO ")
	dest.WriteString(quoteIdent(table))
	dest.WriteString(" (")
	for i, c := range columns {
		if i > 0 {
			dest.WriteString(", ")
		}
		dest.WriteString(quoteIdent(c))
	}
	dest.WriteString(") VALUES\n")
	for ri, row := range rows {
		dest.WriteString("  (")
		for ci, v := range row {
			if ci > 0 {
				dest.WriteString(", ")
			}
			dest.WriteString(sqlLiteral(v))
		}
		dest.WriteString(")")
		if ri < len(rows)-1 {
			dest.WriteString(",\n")
		} else {
			dest.WriteString(";\n\n")
		}
	}
}

// authUserColumns is the fixed column order we emit for the users
// INSERT. Kept as a package var so both the emitter and the tests see
// the same list.
var authUserColumns = []string{
	"id", "email", "phone", "password_hash", "metadata",
	"email_confirmed_at", "phone_confirmed_at", "last_sign_in_at",
	"banned_at", "created_at", "updated_at",
}

// authIdentityColumns is the fixed column order we emit for the
// user_identities INSERT.
var authIdentityColumns = []string{
	"user_id", "provider", "provider_user_id", "identity_data",
	"last_sign_in_at", "created_at", "updated_at",
}

// userRowAsValues expands a translated user row into the []interface{}
// tuple used by emitInsertBatch. Order matches authUserColumns.
func userRowAsValues(u eurobaseUserRow) []interface{} {
	return []interface{}{
		u.ID,
		u.Email,
		u.Phone,
		u.PasswordHash,
		// Metadata is JSONB — emit as string with a ::jsonb cast so
		// Postgres parses it at insert time. sqlLiteral wraps the
		// string in quotes; we append the cast by wrapping.
		jsonbLiteral(u.Metadata),
		u.EmailConfirmedAt,
		u.PhoneConfirmedAt,
		u.LastSignInAt,
		u.BannedAt,
		u.CreatedAt,
		u.UpdatedAt,
	}
}

// identityRowAsValues expands a translated identity row into the
// []interface{} tuple used by emitInsertBatch.
func identityRowAsValues(i eurobaseIdentityRow) []interface{} {
	return []interface{}{
		i.UserID,
		i.Provider,
		i.ProviderUserID,
		jsonbLiteral(i.IdentityData),
		i.LastSignInAt,
		i.CreatedAt,
		i.UpdatedAt,
	}
}

// jsonbValue is the type sqlLiteral recognises to wrap a JSONB
// literal as `'<json>'::jsonb`. Cleaner than plumbing a second-argument
// "type hint" through emitInsertBatch.
type jsonbValue string

func jsonbLiteral(s string) jsonbValue { return jsonbValue(s) }

// typedLiteral carries a text-cast source value + its target Postgres
// type so sqlLiteral can emit `'value'::type`. Used by wrapValue in
// migrate_supabase_data.go for column types the CLI cast to text at
// SELECT time (jsonb / numeric / interval / inet / array types) —
// without this wrapper, pgx would return the raw pgtype struct and
// sqlLiteral's fallback would `%v`-print it into an unparseable
// blob. (#275 review ship-blocker #3/#4.)
type typedLiteral struct {
	value  string
	pgType string
}

// mergeUserAndAppMetadata combines Supabase's `raw_user_meta_data`
// (client-writable) and `raw_app_meta_data` (server-writable) into the
// single JSONB payload Eurobase stores as `users.metadata`. User-meta
// stays at the top level so existing app code that reads
// `user.metadata.display_name` keeps working; app-meta nests under
// `app_metadata` so provider / role / tenant-hint fields aren't lost.
//
// Non-JSON or invalid input is normalised to `{}`. If both are empty,
// the result is `{}`. If either is non-empty but not a JSON object
// (e.g. Supabase stored a bare scalar in it — unusual but possible),
// it gets nested under `_source` so nothing gets silently dropped.
func mergeUserAndAppMetadata(userMeta, appMeta string) string {
	merged := map[string]interface{}{}

	userObj, userOk := parseJSONObject(userMeta)
	if userOk {
		for k, v := range userObj {
			merged[k] = v
		}
	} else if userMeta != "" && userMeta != "null" && userMeta != "{}" {
		merged["_user_metadata_source"] = json.RawMessage(userMeta)
	}

	if appMeta != "" && appMeta != "null" && appMeta != "{}" {
		if appObj, ok := parseJSONObject(appMeta); ok {
			merged["app_metadata"] = appObj
		} else {
			merged["app_metadata"] = json.RawMessage(appMeta)
		}
	}

	if len(merged) == 0 {
		return "{}"
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func parseJSONObject(s string) (map[string]interface{}, bool) {
	if s == "" || s == "null" {
		return nil, false
	}
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, false
	}
	return out, true
}
