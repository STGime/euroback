package cli

// Supabase → Eurobase data translation: the pure parts of
// `eurobase import supabase data` (no database access), so tests can pin
// them: literal formatting, foreign-key ordering, and the mapping of
// `auth.users` / `auth.identities` to Eurobase's `users` /
// `user_identities`.
//
// User ids are kept, so rows and policies that refer to a user
// (`user_id = auth_uid()`) still line up after the import.

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// dataBatchSize is the row-count ceiling per multi-row INSERT. Kept
// as an upper bound alongside `maxBatchBytes` — whichever fires first
// closes the batch. A too-large ceiling risks batches that Postgres's
// parser tolerates but that hit the endpoint's body cap; too-small
// hurts ingest throughput.
const dataBatchSize = 1000

// maxBatchBytes is the SIZE ceiling per multi-row INSERT. The tenant-
// migrations endpoint rejects bodies over 512 KiB
// (`internal/query/tenant_migrations.go:50`); a single INSERT that
// crosses it kills the whole file. 350 KiB leaves headroom for the
// per-file header (~250 B), the writer's rotation slack, and other
// statements the same file may pack alongside this one. (#275 round-2
// review ship-blocker #1 — row-count-only batching overshot on wide
// tables where 1000 rows ≥ 500 KiB.)
const maxBatchBytes = 350 * 1024

// estimateValueBytes returns a conservative upper bound on how many
// bytes `v` will occupy once sqlLiteral emits it. Used by the emitter
// to close a batch before it grows past maxBatchBytes, without having
// to render the row twice.
//
// It's an ESTIMATE (fast, no allocation) — the caller should hold a
// bit of headroom so a bad estimate can't tip us over the cap. The
// literal cases are ordered by likelihood of appearing in Supabase
// row data.
func estimateValueBytes(v interface{}) int {
	if v == nil {
		return 4
	}
	switch t := v.(type) {
	case jsonbValue:
		// Budget the same 25% single-quote doubling as the `string`
		// case — a JSONB payload with embedded `'` chars (error
		// messages, O'Brien-style names) doubles on emit. Without
		// this, an adversarial 100%-quote payload could bloat a
		// 175 KiB batch to 350 KiB (still under the 512 KiB endpoint
		// cap given our 350 KiB flush threshold, but not by much).
		return len(t) + len(t)/4 + 12 // '' + ::jsonb + slack
	case string:
		// Exact: every `'` is doubled.
		return len(t) + strings.Count(t, "'") + 2
	case *string:
		if t == nil {
			return 4
		}
		return len(*t) + strings.Count(*t, "'") + 2
	case time.Time:
		return 45 // RFC3339Nano + ::timestamptz + slack
	case *time.Time:
		if t == nil {
			return 4
		}
		return 45
	case bool:
		return 5
	case int, int32, int64:
		return 22
	case float32, float64:
		return 32
	case []byte:
		return len(t)*2 + 12 // hex + '\x + '::bytea
	default:
		return 96
	}
}

// estimateRowBytes returns a conservative upper bound on how many
// bytes the row will occupy in a multi-row INSERT VALUES tuple.
// Used by emitOneTable / emitAuthData to size-gate the batch. The
// estimate is intentionally biased HIGH — an under-estimate would
// tip a batch over the endpoint's 512 KiB cap, an over-estimate
// merely flushes a little sooner.
func estimateRowBytes(row []interface{}) int {
	// Fixed per-row overhead: `  (` + `)` + `,\n` + a few bytes slack.
	n := 12
	for _, v := range row {
		// Per-column separator `, ` plus the value.
		n += 2 + estimateValueBytes(v)
	}
	return n
}

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
// fail on FK violations. Self-references don't take part in the order;
// emitOneTable handles them.
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
			// A real FK cycle among two or more tables — fail LOUD
			// rather than emit a wrong order that silently blows up
			// at apply time. Identify the actual cycle members (the
			// strongly-connected component) rather than "everything
			// with unresolved deps" — in a shape like `a↔b, c→b`,
			// `c` is stuck but not IN the cycle, and the error
			// message shouldn't mislead the tenant. (#275 round-2
			// review H #2.)
			//
			// Build the sub-graph of unresolved nodes only, then run
			// Tarjan's SCC on it.
			cycle := findCycleMembers(indeg, deps, names)
			return nil, fmt.Errorf("foreign keys form a cycle among tables %v, so no order of tables satisfies them — import these with --table, one at a time, after dropping one of the foreign keys in the project, and add it back afterwards", cycle)
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

// findCycleMembers returns the tables that participate in an actual
// FK cycle among the still-unresolved nodes (indeg > 0). Uses
// Tarjan's SCC algorithm on the sub-graph of unresolved nodes,
// then returns the union of every SCC of size > 1. Downstream nodes
// that merely wait for a cycle to resolve (a `c→b` where `a↔b`)
// aren't returned — they're victims, not perpetrators.
//
// Returns members in name order for deterministic error messages.
func findCycleMembers(indeg map[string]int, deps map[string][]string, allNames []string) []string {
	// Unresolved set — nodes still waiting for a parent.
	unresolved := map[string]bool{}
	for _, n := range allNames {
		if v, ok := indeg[n]; ok && v > 0 {
			unresolved[n] = true
		}
	}

	// Adjacency: parent p → children c where BOTH are unresolved.
	adj := map[string][]string{}
	for parent, children := range deps {
		if !unresolved[parent] {
			continue
		}
		for _, c := range children {
			if unresolved[c] {
				adj[parent] = append(adj[parent], c)
			}
		}
	}

	// Tarjan's SCC state.
	var (
		index    int
		stack    []string
		onStack  = map[string]bool{}
		idx      = map[string]int{}
		lowlink  = map[string]int{}
		cycleSet = map[string]bool{}
	)
	var strongconnect func(v string)
	strongconnect = func(v string) {
		idx[v] = index
		lowlink[v] = index
		index++
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range adj[v] {
			if _, seen := idx[w]; !seen {
				strongconnect(w)
				if lowlink[w] < lowlink[v] {
					lowlink[v] = lowlink[w]
				}
			} else if onStack[w] {
				if idx[w] < lowlink[v] {
					lowlink[v] = idx[w]
				}
			}
		}
		if lowlink[v] == idx[v] {
			// Pop stack down to v — that's one SCC.
			var component []string
			for {
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[top] = false
				component = append(component, top)
				if top == v {
					break
				}
			}
			// A cycle has ≥ 2 members (a lone self-loop was already
			// excluded upstream — see sortTablesByFK's e.from == e.to
			// guard).
			if len(component) > 1 {
				for _, c := range component {
					cycleSet[c] = true
				}
			}
		}
	}
	for _, n := range allNames {
		if !unresolved[n] {
			continue
		}
		if _, seen := idx[n]; !seen {
			strongconnect(n)
		}
	}

	// Fallback: if Tarjan found no size->=2 SCC (shouldn't happen
	// given we only run this branch on a genuine deadlock, but be
	// safe), return every unresolved node so the tenant still gets a
	// clue.
	if len(cycleSet) == 0 {
		for k := range unresolved {
			cycleSet[k] = true
		}
	}

	out := make([]string, 0, len(cycleSet))
	for k := range cycleSet {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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
	DeletedAt         *time.Time // soft delete
	IsAnonymous       bool
	IsSSOUser         bool
}

// supabaseIdentity is one row from `auth.identities` — the OAuth-link
// side table. One user can have many identities (a Google + a GitHub
// login attached to the same account).
type supabaseIdentity struct {
	ID           string     // uuid on current Supabase; kept as the link's id
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
	DisplayName      *string
	AvatarURL        *string
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
	ID             string
	UserID         string
	Provider       string
	ProviderUserID string
	IdentityData   string
	LastSignInAt   *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// maxImportHashCost mirrors the server: sign-in refuses a stored bcrypt
// hash with a higher cost, so importing one would only look like it
// worked.
const maxImportHashCost = 12

// userFlags says what translateAuthUser changed, for the summary.
type userFlags struct {
	noPassword      bool // no password on the source (OAuth / phone / anonymous)
	passwordDropped bool // a hash Eurobase can't use
	temporaryBan    bool // banned until a date
	noLogin         bool // neither email nor phone
}

// translateAuthUser maps one Supabase auth.users row to a Eurobase
// users row. Pure — no DB access.
func translateAuthUser(u supabaseUser, now time.Time) (eurobaseUserRow, userFlags) {
	var flags userFlags

	// Sign-in looks the email up in lower case.
	email := u.Email
	if email != nil {
		e := strings.ToLower(strings.TrimSpace(*email))
		email = &e
		if e == "" {
			email = nil
		}
	}
	// Supabase stores phone numbers without the leading +; Eurobase
	// uses E.164.
	phone := u.Phone
	if phone != nil {
		ph := strings.TrimSpace(*phone)
		if ph != "" && isDigits(ph) {
			ph = "+" + ph
		}
		phone = &ph
		if ph == "" {
			phone = nil
		}
	}
	flags.noLogin = email == nil && phone == nil

	// Password: Supabase and Eurobase both store bcrypt, so the hash is
	// kept as it is. Anything else (or a cost sign-in would refuse)
	// can't be carried over — the user resets the password.
	pw := u.EncryptedPassword
	if pw != nil && *pw == "" {
		pw = nil
	}
	if pw == nil {
		flags.noPassword = true
	} else if cost, err := bcrypt.Cost([]byte(*pw)); err != nil || cost > maxImportHashCost {
		pw = nil
		flags.passwordDropped = true
	}

	// A ban that has run out is not a ban. Eurobase has no "until": a
	// ban that is still running becomes a ban without an end.
	var bannedAt *time.Time
	if u.BannedUntil != nil && u.BannedUntil.After(now) {
		t := now.UTC().Truncate(time.Second)
		bannedAt = &t
		flags.temporaryBan = true
	}

	// Metadata: user metadata stays at the top level (app code reads it
	// there), app metadata goes under `_app_metadata`.
	metadata := mergeUserAndAppMetadata(u.RawUserMetaData, u.RawAppMetaData)
	userMeta, _ := parseJSONObject(u.RawUserMetaData)

	updatedAt := u.CreatedAt
	if u.UpdatedAt != nil {
		updatedAt = *u.UpdatedAt
	}

	return eurobaseUserRow{
		ID:               u.ID,
		Email:            email,
		Phone:            phone,
		PasswordHash:     pw,
		DisplayName:      firstString(userMeta, "display_name", "full_name", "name"),
		AvatarURL:        firstString(userMeta, "avatar_url", "picture"),
		Metadata:         metadata,
		EmailConfirmedAt: u.EmailConfirmedAt,
		PhoneConfirmedAt: u.PhoneConfirmedAt,
		LastSignInAt:     u.LastSignInAt,
		BannedAt:         bannedAt,
		CreatedAt:        u.CreatedAt,
		UpdatedAt:        updatedAt,
	}, flags
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// firstString returns the first of keys that holds a non-empty string.
func firstString(m map[string]interface{}, keys ...string) *string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return &v
		}
	}
	return nil
}

// authPlan is what the import writes for auth, and what it tells the
// developer about it.
type authPlan struct {
	users      []eurobaseUserRow
	identities []eurobaseIdentityRow
	notes      []string
}

// planAuthImport decides which users and OAuth links are imported and
// how. Pure — no DB access.
//
// Left out: users deleted on Supabase and SSO users (their sign-in
// belongs to an identity provider Eurobase doesn't have a link for; an
// SSO user may also share an email with a normal user). Everyone else is
// imported, including users without email or phone (anonymous sign-ins),
// because rows in the app's tables refer to them.
func planAuthImport(users []supabaseUser, identities []supabaseIdentity, now time.Time) (authPlan, error) {
	var plan authPlan
	var deleted, sso, noLogin, noPassword, pwDropped, tempBans int
	imported := map[string]bool{}
	byEmail := map[string]string{}
	byPhone := map[string]string{}
	for _, u := range users {
		if u.DeletedAt != nil {
			deleted++
			continue
		}
		if u.IsSSOUser {
			sso++
			continue
		}
		row, flags := translateAuthUser(u, now)
		if row.Email != nil {
			if other, dup := byEmail[*row.Email]; dup {
				return plan, fmt.Errorf("users %s and %s have the same email address once it is lower-cased; Eurobase needs it unique — change one of them on Supabase and run again", other, u.ID)
			}
			byEmail[*row.Email] = u.ID
		}
		if row.Phone != nil {
			if other, dup := byPhone[*row.Phone]; dup {
				return plan, fmt.Errorf("users %s and %s have the same phone number; Eurobase needs it unique — change one of them on Supabase and run again", other, u.ID)
			}
			byPhone[*row.Phone] = u.ID
		}
		if flags.noLogin {
			noLogin++
		} else if flags.noPassword {
			noPassword++
		}
		if flags.passwordDropped {
			pwDropped++
		}
		if flags.temporaryBan {
			tempBans++
		}
		imported[u.ID] = true
		plan.users = append(plan.users, row)
	}

	var orphanLinks int
	unsupported := map[string]int{}
	for _, i := range identities {
		// Supabase also keeps an "identity" for email and phone
		// sign-in; on Eurobase those are the user row itself.
		if i.Provider == "email" || i.Provider == "phone" {
			continue
		}
		if !imported[i.UserID] {
			orphanLinks++
			continue
		}
		provider, ok := eurobaseProviderFor[i.Provider]
		if !ok {
			unsupported[i.Provider]++
			continue
		}
		i.Provider = provider
		plan.identities = append(plan.identities, translateAuthIdentity(i))
	}

	add := func(n int, format string) {
		if n > 0 {
			plan.notes = append(plan.notes, fmt.Sprintf(format, n))
		}
	}
	add(deleted, "%d user(s) deleted on Supabase are not imported; rows in your tables that still refer to them will fail to apply")
	add(sso, "%d SSO user(s) are not imported (their sign-in belongs to your identity provider); rows that refer to them will fail to apply")
	add(noLogin, "%d user(s) have neither email nor phone (anonymous sign-ins): imported so their rows stay valid, but they can't sign in again — their Supabase session doesn't carry over")
	add(noPassword, "%d user(s) have no password (OAuth or phone sign-in): they sign in the same way as before, once that provider is set up in the project")
	add(pwDropped, "%d user(s) have a password hash Eurobase can't use (not bcrypt, or a cost above 12): imported without a password — they use \"forgot password\"")
	add(tempBans, "%d user(s) are banned until a date on Supabase: imported as banned, without an end date — lift the ban in the console when it is due")
	add(orphanLinks, "%d OAuth link(s) belong to users that are not imported; skipped")
	if len(unsupported) > 0 {
		var parts []string
		for name, n := range unsupported {
			parts = append(parts, fmt.Sprintf("%s (%d)", name, n))
		}
		sort.Strings(parts)
		plan.notes = append(plan.notes, fmt.Sprintf("OAuth links for providers Eurobase doesn't offer are not imported: %s — these users sign in with a magic link or \"forgot password\" on their email address", strings.Join(parts, ", ")))
	}
	if len(plan.identities) > 0 {
		plan.notes = append(plan.notes, fmt.Sprintf("%d OAuth link(s) imported: set each provider up in the project with the same OAuth app (client ID) as on Supabase — Microsoft and Apple give a user a different id per app, so with a new app those users aren't recognised", len(plan.identities)))
	}
	return plan, nil
}

// eurobaseProviderFor maps Supabase's provider names to the names
// Eurobase's sign-in looks links up by. A provider that isn't listed has
// no sign-in on Eurobase. Supabase's older "linkedin" provider is left
// out on purpose: its user ids are not the OpenID Connect ones.
var eurobaseProviderFor = map[string]string{
	"google":        "google",
	"github":        "github",
	"apple":         "apple",
	"azure":         "microsoft",
	"linkedin_oidc": "linkedin",
}

// translateAuthIdentity maps one Supabase auth.identities row to a
// Eurobase user_identities row. Pure — no DB access.
func translateAuthIdentity(i supabaseIdentity) eurobaseIdentityRow {
	// The id is always written: the column's default is a function the
	// migration's database role may not be allowed to call. Older
	// Supabase projects have a text id here; those get a stable uuid.
	id := i.ID
	if _, err := uuid.Parse(id); err != nil {
		id = uuid.NewSHA1(uuid.NameSpaceOID, []byte(i.UserID+"\x00"+i.Provider+"\x00"+i.ProviderID)).String()
	}
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
		ID:             id,
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
	emitInsert(dest, table, columns, rows, false)
}

// emitInsert is emitInsertBatch with the option a table with a
// GENERATED ALWAYS identity column needs: without OVERRIDING SYSTEM
// VALUE, Postgres refuses an INSERT that names the column.
func emitInsert(dest *strings.Builder, table string, columns []string, rows [][]interface{}, overridingSystemValue bool) {
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
	dest.WriteString(")")
	if overridingSystemValue {
		dest.WriteString(" OVERRIDING SYSTEM VALUE")
	}
	dest.WriteString(" VALUES\n")
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
			dest.WriteString(";\n")
		}
	}
}

// authUserColumns is the fixed column order we emit for the users
// INSERT. Kept as a package var so both the emitter and the tests see
// the same list.
var authUserColumns = []string{
	"id", "email", "phone", "password_hash", "display_name", "avatar_url", "metadata",
	"email_confirmed_at", "phone_confirmed_at", "last_sign_in_at",
	"banned_at", "created_at", "updated_at",
}

// authIdentityColumns is the fixed column order we emit for the
// user_identities INSERT.
var authIdentityColumns = []string{
	"id", "user_id", "provider", "provider_user_id", "identity_data",
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
		u.DisplayName,
		u.AvatarURL,
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
		i.ID,
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

// mergeUserAndAppMetadata combines Supabase's `raw_user_meta_data`
// (client-writable) and `raw_app_meta_data` (server-writable) into the
// single JSONB payload Eurobase stores as `users.metadata`. User-meta
// stays at the top level so existing app code that reads
// `user.metadata.display_name` keeps working; app-meta nests under
// `_app_metadata` (underscored to avoid collision with an existing
// user-supplied `app_metadata` key in the source).
//
// Non-JSON or invalid input is preserved as a JSON string under
// `_user_metadata_source` / the same nested key — rather than
// silently dropped. Both marshal cleanly on the outer json.Marshal
// (the previous version used json.RawMessage(bytes) which would
// re-marshal the invalid JSON as-is and blow up on the outer
// json.Marshal, losing app-meta too). (#275 round-2 M #3 + L #7.)
func mergeUserAndAppMetadata(userMeta, appMeta string) string {
	merged := map[string]interface{}{}

	userObj, userOk := parseJSONObject(userMeta)
	if userOk {
		for k, v := range userObj {
			merged[k] = v
		}
	} else if userMeta != "" && userMeta != "null" && userMeta != "{}" {
		merged["_user_metadata_source"] = safeJSONPayload(userMeta)
	}

	if appMeta != "" && appMeta != "null" && appMeta != "{}" {
		if appObj, ok := parseJSONObject(appMeta); ok {
			merged["_app_metadata"] = appObj
		} else {
			merged["_app_metadata"] = safeJSONPayload(appMeta)
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

// safeJSONPayload wraps a raw source blob so the outer json.Marshal
// can always encode it. Valid JSON → RawMessage passes through
// verbatim. Invalid JSON → wrap as a plain string so the outer marshal
// still succeeds. Either way, nothing gets silently lost.
func safeJSONPayload(raw string) interface{} {
	if json.Valid([]byte(raw)) {
		return json.RawMessage(raw)
	}
	return raw
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
