// Statement guard for customer SQL (ctx.db.sql). Mirrors
// internal/query/privilege_guard.go: role, session, search_path,
// privilege and ownership management is never allowed from function
// code — the runner sets the tenant role and search_path itself.
//
// This is a lexical BACKSTOP, not the primary control: it cannot see
// inside function bodies or dynamically built SQL. On this path it also
// refuses DO blocks and CREATE FUNCTION/PROCEDURE (never needed from an
// edge function) to shrink that blind spot. The real boundary is the
// database role the SQL runs as.

/** UUID shape check for HMAC-verified header values. */
export const uuidRe = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export interface SqlWord {
  kw: string; // lower-cased keyword; "" for quoted identifiers
  name: string; // lower-cased identifier value (quoted or not)
}

// PostgreSQL identifiers: letters, '_' and any non-ASCII char may start
// one; digits and '$' may also continue one.
const identStart = /[A-Za-z_\u0080-￿]/;
const identCont = /[A-Za-z0-9_$\u0080-￿]/;

/** Skips a quoted region starting at sql[i] === q; returns [nextIndex, content]. */
function skipQuoted(sql: string, i: number, q: string, backslash: boolean): [number, string] {
  const n = sql.length;
  let out = "";
  i++;
  while (i < n) {
    const c = sql[i];
    if (backslash && c === "\\" && i + 1 < n) {
      out += sql[i + 1];
      i += 2;
    } else if (c === q && sql[i + 1] === q) {
      out += q;
      i += 2;
    } else if (c === q) {
      return [i + 1, out];
    } else {
      out += c;
      i++;
    }
  }
  return [n, out];
}

class UnicodeIdentError extends Error {}

/** Splits SQL into statements of words; strings, comments and dollar quotes are skipped. */
export function sqlStatements(sql: string): SqlWord[][] {
  const stmts: SqlWord[][] = [];
  let cur: SqlWord[] = [];
  const n = sql.length;
  let i = 0;
  while (i < n) {
    const c = sql[i];
    if (c === "'") {
      [i] = skipQuoted(sql, i, "'", false);
    } else if (c === '"') {
      let v: string;
      [i, v] = skipQuoted(sql, i, '"', false);
      cur.push({ kw: "", name: v.toLowerCase() });
    } else if (c === ";") {
      stmts.push(cur);
      cur = [];
      i++;
    } else if (c === "-" && sql[i + 1] === "-") {
      while (i < n && sql[i] !== "\n") i++;
    } else if (c === "/" && sql[i + 1] === "*") {
      let depth = 1;
      i += 2;
      while (i < n && depth > 0) {
        if (sql[i] === "/" && sql[i + 1] === "*") { depth++; i += 2; }
        else if (sql[i] === "*" && sql[i + 1] === "/") { depth--; i += 2; }
        else i++;
      }
    } else if (c === "$") {
      const m = /^\$([A-Za-z_][A-Za-z0-9_]*)?\$/.exec(sql.slice(i));
      if (m) {
        const tag = m[0];
        const end = sql.indexOf(tag, i + tag.length);
        i = end < 0 ? n : end + tag.length;
      } else {
        i++;
      }
    } else if (identStart.test(c)) {
      let j = i + 1;
      while (j < n && identCont.test(sql[j])) j++;
      const word = sql.slice(i, j);
      i = j;
      // String / identifier prefixes: E'..' (backslash escapes),
      // U&'..' / U&".." (unicode escapes, not decoded).
      if ((word === "E" || word === "e") && sql[i] === "'") {
        [i] = skipQuoted(sql, i, "'", true);
        continue;
      }
      if ((word === "U" || word === "u") && sql[i] === "&" && (sql[i + 1] === "'" || sql[i + 1] === '"')) {
        if (sql[i + 1] === '"') throw new UnicodeIdentError();
        [i] = skipQuoted(sql, i + 1, "'", false);
        continue;
      }
      const lw = word.toLowerCase();
      cur.push({ kw: lw, name: lw });
    } else {
      i++;
    }
  }
  stmts.push(cur);
  return stmts;
}

/** Returns an error message, or null when the SQL is acceptable. */
export function privilegeStatementError(sql: string): string | null {
  const deny = (what: string) =>
    `${what} is not allowed in function SQL: role, session and privilege management is handled by the platform`;
  let stmts: SqlWord[][];
  try {
    stmts = sqlStatements(sql);
  } catch (e) {
    if (e instanceof UnicodeIdentError) return deny('U&"…" identifiers');
    throw e;
  }

  for (const st of stmts) {
    const kw = (i: number) => (i >= 0 && i < st.length ? st[i].kw : "");
    const first = kw(0);

    // Function-path extras: no anonymous blocks or new routines.
    if (first === "do") return deny("DO");
    if (first === "create") {
      let j = 1;
      if (kw(j) === "or" && kw(j + 1) === "replace") j += 2;
      if (kw(j) === "function" || kw(j) === "procedure") return deny(`CREATE ${kw(j).toUpperCase()}`);
    }

    // SET / RESET as a command (statement start only, so UPDATE … SET
    // role = … stays allowed).
    if (first === "set" || first === "reset") {
      let j = 1;
      while (kw(j) === "local" || kw(j) === "session") j++;
      const target = j < st.length ? st[j].name : "";
      if (["role", "all", "search_path", "session", "authorization", "session_authorization"].includes(target)) {
        return deny(`${first} ${target}`.toUpperCase());
      }
    }

    for (let i = 0; i < st.length; i++) {
      const w = st[i];
      if (["search_path", "set_config", "session_authorization", "pg_settings"].includes(w.name)) {
        return deny(w.name.toUpperCase());
      }
      switch (w.kw) {
        case "grant":
        case "revoke":
        case "reassign":
          return deny(w.kw.toUpperCase());
        case "authorization":
          return deny("AUTHORIZATION");
        case "privileges":
          if (kw(i - 1) === "default") return deny("ALTER DEFAULT PRIVILEGES");
          break;
        case "role":
        case "user":
        case "group":
          if (["create", "alter", "drop"].includes(kw(i - 1))) return deny(`${kw(i - 1)} ${w.kw}`.toUpperCase());
          break;
        case "owned":
          if (kw(i - 1) === "drop") return deny("DROP OWNED");
          break;
        case "owner":
          if (kw(i + 1) === "to") return deny("OWNER TO");
          break;
        case "definer":
          if (kw(i - 1) === "security") return deny("SECURITY DEFINER");
          break;
      }
    }
  }
  return null;
}
