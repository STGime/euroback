// Statement guard for customer SQL (ctx.db.sql). Mirrors
// internal/query/privilege_guard.go: role, session, search_path,
// privilege and ownership management is never allowed from function
// code — the runner sets the tenant role and search_path itself, and
// customer SQL must not be able to change either.
//
// Comment-, string-, dollar-quote- and quoted-identifier-aware, so
// keywords inside literals or "quoted" names are ignored.

/** UUID shape check for HMAC-verified header values. */
export const uuidRe = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Lower-cased keyword/identifier tokens; quoted identifiers become "". */
export function sqlWords(sql: string): string[] {
  const out: string[] = [];
  const n = sql.length;
  let i = 0;
  while (i < n) {
    const c = sql[i];
    if (c === "'") {
      i++;
      while (i < n) {
        if (sql[i] === "'") {
          if (sql[i + 1] === "'") { i += 2; continue; }
          i++;
          break;
        }
        i++;
      }
    } else if (c === '"') {
      i++;
      while (i < n) {
        if (sql[i] === '"') {
          if (sql[i + 1] === '"') { i += 2; continue; }
          i++;
          break;
        }
        i++;
      }
      out.push("");
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
    } else if (/[A-Za-z_]/.test(c)) {
      let j = i + 1;
      while (j < n && /[A-Za-z0-9_$]/.test(sql[j])) j++;
      out.push(sql.slice(i, j).toLowerCase());
      i = j;
    } else {
      i++;
    }
  }
  return out;
}

/** Returns an error message, or null when the SQL is acceptable. */
export function privilegeStatementError(sql: string): string | null {
  const w = sqlWords(sql);
  const at = (i: number) => (i >= 0 && i < w.length ? w[i] : "");
  const deny = (what: string) =>
    `${what} is not allowed in function SQL: role, session and privilege management is handled by the platform`;
  for (let i = 0; i < w.length; i++) {
    switch (w[i]) {
      case "grant":
      case "revoke":
      case "reassign":
      case "set_config":
      case "search_path":
        return deny(w[i].toUpperCase());
      case "privileges":
        if (at(i - 1) === "default") return deny("ALTER DEFAULT PRIVILEGES");
        break;
      case "authorization":
        if (at(i - 1) === "session") return deny("SESSION AUTHORIZATION");
        break;
      case "role":
      case "user":
      case "group": {
        const prev = at(i - 1);
        if (w[i] === "role" && ["set", "reset", "local", "session"].includes(prev)) {
          return deny("SET/RESET ROLE");
        }
        if (["create", "alter", "drop"].includes(prev)) return deny(`${prev} ${w[i]}`.toUpperCase());
        break;
      }
      case "all":
        if (at(i - 1) === "reset") return deny("RESET ALL");
        break;
      case "owned":
        if (at(i - 1) === "drop") return deny("DROP OWNED");
        break;
      case "owner":
        if (at(i + 1) === "to") return deny("OWNER TO");
        break;
      case "definer":
        if (at(i - 1) === "security") return deny("SECURITY DEFINER");
        break;
    }
  }
  return null;
}
