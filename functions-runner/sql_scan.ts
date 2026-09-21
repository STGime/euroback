// Lexical identifier scanner used to fence ctx.db.asService() from the
// per-tenant platform system tables (users, refresh_tokens, email_tokens,
// vault_secrets, storage_objects) which live in the tenant schema and
// carry RLS policies keyed on is_service_role(). Because <schema>_func
// already has DML grants on them (migration 000047), a JWT-verified
// asService() call would otherwise reach password_hash reads, refresh
// token forgery, and password_hash overwrite — an accidental escalation
// nothing about the primitive's intent (approve tenant-owned rows)
// requires.
//
// Mirrors internal/query/cross_schema.go scanIdentifiersAndDots
// byte-for-byte so the runner's fence has the same shape (skips string
// literals, comments, dollar-quoted bodies) as the SDK-path
// guardSDKSQLTables in internal/query/column_denylist.go.
//
// The Go side is the source of truth: any change to the scanner (new
// dollar-tag rules, new comment forms, etc.) MUST land on both sides.

export const enum TokKind {
  Ident = 0,
  Dot = 1,
}

export interface Token {
  kind: TokKind;
  value: string; // empty for Dot
}

function isIdentStart(c: string): boolean {
  return c === "_" || (c >= "a" && c <= "z") || (c >= "A" && c <= "Z");
}

function isIdentCont(c: string): boolean {
  return isIdentStart(c) || (c >= "0" && c <= "9");
}

function isDollarTagChar(c: string, first: boolean): boolean {
  if (first) {
    return c === "_" || (c >= "a" && c <= "z") || (c >= "A" && c <= "Z");
  }
  return isIdentCont(c);
}

// scanIdentifiersAndDots produces a token stream of identifiers and
// dots from `sql`, skipping single-quoted strings, double-quoted
// identifiers (emitted as idents with the escape rule "" → "), line
// comments (-- …), block comments (/* … */, nested), and dollar-quoted
// bodies ($$ … $$ or $tag$ … $tag$).
export function scanIdentifiersAndDots(sql: string): Token[] {
  const out: Token[] = [];
  const n = sql.length;
  let i = 0;
  while (i < n) {
    const c = sql[i];
    if (c === "'") {
      i++;
      while (i < n) {
        if (sql[i] === "'") {
          if (i + 1 < n && sql[i + 1] === "'") {
            i += 2;
            continue;
          }
          i++;
          break;
        }
        i++;
      }
      continue;
    }
    if (c === '"') {
      i++;
      let value = "";
      while (i < n) {
        if (sql[i] === '"') {
          if (i + 1 < n && sql[i + 1] === '"') {
            value += '"';
            i += 2;
            continue;
          }
          i++;
          break;
        }
        value += sql[i];
        i++;
      }
      out.push({ kind: TokKind.Ident, value });
      continue;
    }
    if (c === "-" && i + 1 < n && sql[i + 1] === "-") {
      i += 2;
      while (i < n && sql[i] !== "\n") i++;
      continue;
    }
    if (c === "/" && i + 1 < n && sql[i + 1] === "*") {
      i += 2;
      let depth = 1;
      while (i < n && depth > 0) {
        if (i + 1 < n && sql[i] === "/" && sql[i + 1] === "*") {
          depth++;
          i += 2;
          continue;
        }
        if (i + 1 < n && sql[i] === "*" && sql[i + 1] === "/") {
          depth--;
          i += 2;
          continue;
        }
        i++;
      }
      continue;
    }
    if (c === "$") {
      let tagEnd = i + 1;
      while (tagEnd < n && isDollarTagChar(sql[tagEnd], tagEnd === i + 1)) {
        tagEnd++;
      }
      if (tagEnd >= n || sql[tagEnd] !== "$") {
        // Stray $ — not a dollar-quoted string.
        i++;
        continue;
      }
      const tag = sql.slice(i, tagEnd + 1);
      const closer = sql.indexOf(tag, tagEnd + 1);
      if (closer < 0) return out; // unterminated; stop scanning
      i = closer + tag.length;
      continue;
    }
    if (c === ".") {
      out.push({ kind: TokKind.Dot, value: "" });
      i++;
      continue;
    }
    if (isIdentStart(c)) {
      const start = i;
      i++;
      while (i < n && isIdentCont(sql[i])) i++;
      out.push({ kind: TokKind.Ident, value: sql.slice(start, i) });
      continue;
    }
    i++;
  }
  return out;
}

// PLATFORM_SYSTEM_TABLES is the set of per-tenant tables the platform
// manages and whose RLS policies gate on is_service_role() — meaning
// ctx.db.asService() would flip them open. Mirrors the SDK-path denylist
// in internal/query/column_denylist.go sqlPathDeniedTables, plus
// storage_objects (which lives in the tenant schema and carries the
// same platform-managed contract; see CLAUDE.md).
//
// Kept lowercase; identifiers are compared case-insensitively per
// PostgreSQL's unquoted-identifier folding rule.
export const PLATFORM_SYSTEM_TABLES: ReadonlySet<string> = new Set([
  "users",
  "refresh_tokens",
  "email_tokens",
  "vault_secrets",
  "storage_objects",
]);

// findPlatformSystemTableRef returns the offending table name if `sql`
// references any platform system table by identifier, else null. Used
// by the runner to refuse an asService() query that would exfiltrate
// password hashes or forge refresh tokens through the RLS policy the
// service branch unlocks. Identifier-token scan, so a matching string
// literal or comment does not false-positive.
export function findPlatformSystemTableRef(sql: string): string | null {
  for (const tok of scanIdentifiersAndDots(sql)) {
    if (tok.kind !== TokKind.Ident) continue;
    if (PLATFORM_SYSTEM_TABLES.has(tok.value.toLowerCase())) {
      return tok.value.toLowerCase();
    }
  }
  return null;
}
