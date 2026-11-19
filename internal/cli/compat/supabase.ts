// Supabase client compatibility for Eurobase edge functions.
//
// `eurobase edge-functions deploy` bundles this file in place of
// @supabase/supabase-js when a function imports it, so a function written
// for Supabase keeps its `createClient(...)` code. There is no HTTP client
// underneath: every call becomes SQL through ctx.db (or ctx.storage), the
// way a hand-written Eurobase function would do it.
//
// What a client may do follows the key it was created with:
//   - the service role key  → SQL with the service role
//     (ctx.db.asService(); the function needs the allow-service-role
//     setting when a signed-in user calls it)
//   - any other key         → SQL as the caller, under your RLS policies
//
// Supported: auth.getUser / getSession / admin.getUserById, from(table)
// with select / insert / update / upsert / delete and the common filters,
// rpc(), and storage upload / signed URLs / remove. Anything else throws
// an error that names the call, so a gap shows up in the function's log
// instead of as a silently empty result.

// deno-lint-ignore-file no-explicit-any

export const ANON_KEY = "eurobase-anon";

function scope(): any {
  const e = (globalThis as any).__eurobase;
  if (!e || !e.ctx) {
    throw new Error("Supabase compatibility: no request is being handled (the client can only be used while the function runs)");
  }
  return e;
}

function current(): any {
  return scope().ctx;
}

// The value Deno.env.get("SUPABASE_SERVICE_ROLE_KEY") has in this request.
// The wrapper makes a new random one for every request (unless the
// project set its own), so nothing a caller sends can equal it.
function isServiceRoleKey(key: unknown): boolean {
  const k = scope().serviceKey;
  return typeof k === "string" && k.length >= 16 && key === k;
}

function unsupported(what: string, instead: string): never {
  throw new Error(`Supabase compatibility: ${what} is not available on Eurobase — ${instead}`);
}

export interface CompatError {
  message: string;
  details: string;
  hint: string;
  code: string;
}

function toError(e: unknown, code = ""): CompatError {
  const message = e instanceof Error ? e.message : String(e);
  return { message, details: "", hint: "", code };
}

// ── SQL helpers ─────────────────────────────────────────────────────

export function quoteIdent(name: string): string {
  if (typeof name !== "string" || name === "" || name.includes("\u0000")) {
    throw new Error(`Supabase compatibility: invalid name ${JSON.stringify(name)}`);
  }
  return '"' + name.replaceAll('"', '""') + '"';
}

// An rpc argument goes to the database as it is: the database decides its
// type from the function's parameter, and the driver writes the value in
// that type's form (a JS object for jsonb, an array for an array, true
// for a boolean).
export function toParam(v: unknown): unknown {
  if (v === undefined) return null;
  if (typeof v === "bigint") return String(v);
  return v;
}

// jsonValue is how a value is written into the JSON text that carries
// rows and filter values.
function jsonValue(v: unknown): unknown {
  if (v === undefined) return null;
  if (typeof v === "bigint") return String(v);
  return v;
}

// parseInList reads the value of an "in" filter: an array, or the
// PostgREST text form "(1,2,3)" / '("a","b")'.
export function parseInList(value: unknown): unknown[] {
  if (Array.isArray(value)) return value;
  if (typeof value === "string") {
    const m = value.trim().match(/^\((.*)\)$/s);
    if (m) {
      if (m[1].trim() === "") return [];
      return m[1].split(",").map((item) => {
        const t = item.trim();
        return t.length >= 2 && t.startsWith('"') && t.endsWith('"') ? t.slice(1, -1).replaceAll('\\"', '"') : t;
      });
    }
  }
  throw new Error('Supabase compatibility: an "in" filter takes an array or the text form "(a,b,c)"');
}

// parseColumns turns a PostgREST select list ("id, name, total:amount")
// into SQL. Embedded resources ("orders(*)") and JSON paths need a join or
// an expression: write those as SQL.
export function parseColumns(columns: string | undefined): string {
  const text = (columns ?? "*").trim();
  if (text === "" || text === "*") return "*";
  if (/[()]/.test(text)) {
    unsupported(
      `select(${JSON.stringify(text)}) with an embedded table`,
      "join the tables in SQL with ctx.db.sql, or select from a view",
    );
  }
  return text.split(",").map((part) => {
    let p = part.trim();
    if (p === "*") return "*";
    let alias = "";
    const colon = p.indexOf(":");
    if (colon > 0 && p[colon + 1] !== ":") {
      alias = p.slice(0, colon).trim();
      p = p.slice(colon + 1).trim();
    }
    let cast = "";
    const dc = p.indexOf("::");
    if (dc > 0) {
      cast = p.slice(dc + 2).trim();
      p = p.slice(0, dc).trim();
      // One type name, nothing else: this text becomes part of the SQL.
      if (!/^[a-zA-Z_][a-zA-Z0-9_]*(\[\])?$/.test(cast)) {
        throw new Error(`Supabase compatibility: invalid cast in select(${JSON.stringify(text)}) — a cast takes one type name`);
      }
    }
    if (!/^[A-Za-z_][A-Za-z0-9_$]*$/.test(p)) {
      unsupported(`select(${JSON.stringify(text)})`, "only plain column names, aliases and casts are understood; write the query as SQL with ctx.db.sql");
    }
    let sql = quoteIdent(p);
    if (cast) sql += "::" + cast;
    if (alias) sql += " AS " + quoteIdent(alias);
    else if (cast) sql += " AS " + quoteIdent(p);
    return sql;
  }).join(", ");
}

type Runner = (query: string, params: unknown[]) => Promise<any[]>;

const OPERATORS: Record<string, string> = {
  eq: "=",
  neq: "<>",
  gt: ">",
  gte: ">=",
  lt: "<",
  lte: "<=",
  like: "LIKE",
  ilike: "ILIKE",
};

// QueryBuilder collects one PostgREST-style request and runs it as one SQL
// statement when awaited.
export class QueryBuilder implements PromiseLike<any> {
  private op: "select" | "insert" | "update" | "upsert" | "delete" = "select";
  private columns: string | undefined;
  private returning: string | undefined | null = null; // null: no RETURNING asked for
  private values: any = null;
  private upsertOpts: { onConflict?: string; ignoreDuplicates?: boolean } = {};
  private where: string[] = [];
  private params: unknown[] = [];
  private orders: string[] = [];
  private limitN: number | null = null;
  private offsetN: number | null = null;
  private mode: "many" | "single" | "maybeSingle" = "many";
  private countMode: string | null = null;
  private head = false;
  private throwing = false;

  constructor(private run: Runner, private table: string) {}

  // ── what to do ──

  select(columns?: string, opts?: { count?: string; head?: boolean }): this {
    if (this.op === "select") {
      this.columns = columns;
      if (opts?.count) this.countMode = opts.count;
      if (opts?.head) this.head = true;
    } else {
      this.returning = columns;
    }
    return this;
  }

  insert(values: any, _opts?: unknown): this {
    this.op = "insert";
    this.values = values;
    return this;
  }

  upsert(values: any, opts?: { onConflict?: string; ignoreDuplicates?: boolean }): this {
    this.op = "upsert";
    this.values = values;
    this.upsertOpts = opts ?? {};
    return this;
  }

  update(values: any, _opts?: unknown): this {
    this.op = "update";
    this.values = values;
    return this;
  }

  delete(_opts?: unknown): this {
    this.op = "delete";
    return this;
  }

  // ── filters ──

  private col(name: string): string {
    if (!/^[A-Za-z_][A-Za-z0-9_$]*$/.test(name)) {
      unsupported(`a filter on ${JSON.stringify(name)}`, "only plain column names are understood (no JSON paths or embedded tables); write the query as SQL with ctx.db.sql");
    }
    return quoteIdent(this.table) + "." + quoteIdent(name);
  }

  // A filter value travels inside a small JSON text and the database
  // reads it as the column's own type, the way it reads inserted rows.
  // So "true" from a query string meets a boolean column as true, an
  // object meets a jsonb column as that object, and nothing depends on
  // how the driver would write a value of a type it has to guess.
  private value(column: string, v: unknown): string {
    this.col(column);
    this.params.push(JSON.stringify({ [column]: jsonValue(v) }));
    const t = quoteIdent(this.table);
    return `(json_populate_record(null::${t}, $${this.params.length}::text::json)).${quoteIdent(column)}`;
  }

  private compare(column: string, op: string, value: unknown, negate = false): this {
    let sql: string;
    if (op in OPERATORS) {
      sql = `${this.col(column)} ${OPERATORS[op]} ${this.value(column, value)}`;
    } else if (op === "is") {
      const word = value === null || value === "null" ? "NULL" : value === true || value === "true" ? "TRUE" : value === false || value === "false" ? "FALSE" : null;
      if (!word) throw new Error("Supabase compatibility: is() takes null, true or false");
      sql = `${this.col(column)} IS ${word}`;
    } else if (op === "in") {
      const list = parseInList(value);
      if (list.length === 0) {
        sql = "FALSE";
      } else {
        const t = quoteIdent(this.table);
        const c = quoteIdent(column);
        this.col(column);
        this.params.push(JSON.stringify(list.map((v) => ({ [column]: jsonValue(v) }))));
        sql = `${this.col(column)} IN (SELECT ${c} FROM json_populate_recordset(null::${t}, $${this.params.length}::text::json))`;
      }
    } else {
      unsupported(`the "${op}" filter`, "write the query as SQL with ctx.db.sql");
    }
    this.where.push(negate ? `NOT (${sql})` : sql);
    return this;
  }

  eq(c: string, v: unknown) { return this.compare(c, "eq", v); }
  neq(c: string, v: unknown) { return this.compare(c, "neq", v); }
  gt(c: string, v: unknown) { return this.compare(c, "gt", v); }
  gte(c: string, v: unknown) { return this.compare(c, "gte", v); }
  lt(c: string, v: unknown) { return this.compare(c, "lt", v); }
  lte(c: string, v: unknown) { return this.compare(c, "lte", v); }
  like(c: string, v: string) { return this.compare(c, "like", v); }
  ilike(c: string, v: string) { return this.compare(c, "ilike", v); }
  is(c: string, v: unknown) { return this.compare(c, "is", v); }
  in(c: string, v: unknown[]) { return this.compare(c, "in", v); }
  not(c: string, op: string, v: unknown) { return this.compare(c, op, v, true); }
  filter(c: string, op: string, v: unknown) { return this.compare(c, op, v); }
  match(query: Record<string, unknown>) {
    for (const [c, v] of Object.entries(query)) this.compare(c, "eq", v);
    return this;
  }
  or(filters: string): this {
    return unsupported(`or(${JSON.stringify(filters)})`, "write the query as SQL with ctx.db.sql");
  }
  contains(c: string, _v: unknown): this {
    return unsupported(`contains() on ${c}`, "write the query as SQL with ctx.db.sql (the @> operator)");
  }
  textSearch(c: string, _q: string): this {
    return unsupported(`textSearch() on ${c}`, "write the query as SQL with ctx.db.sql");
  }

  // ── shape of the result ──

  order(column: string, opts?: { ascending?: boolean; nullsFirst?: boolean }): this {
    let sql = this.col(column) + (opts?.ascending === false ? " DESC" : " ASC");
    if (opts?.nullsFirst === true) sql += " NULLS FIRST";
    if (opts?.nullsFirst === false) sql += " NULLS LAST";
    this.orders.push(sql);
    return this;
  }

  limit(n: number): this {
    this.limitN = wholeNumber(n, "limit()");
    return this;
  }

  range(from: number, to: number): this {
    this.offsetN = wholeNumber(from, "range()");
    this.limitN = Math.max(0, wholeNumber(to, "range()") - this.offsetN + 1);
    return this;
  }

  single(): this {
    this.mode = "single";
    return this;
  }

  maybeSingle(): this {
    this.mode = "maybeSingle";
    return this;
  }

  throwOnError(): this {
    this.throwing = true;
    return this;
  }

  // Accepted and ignored: they only affect typing or the HTTP request.
  returns(): this { return this; }
  overrideTypes(): this { return this; }
  abortSignal(_s?: unknown): this { return this; }

  csv(): this {
    return unsupported("csv()", "format the rows in your code");
  }

  // ── building ──

  private whereSQL(): string {
    return this.where.length ? " WHERE " + this.where.join(" AND ") : "";
  }

  // A key whose value is undefined is not part of the row, as in
  // supabase-js (JSON has no undefined): update({ a: body.a }) must not
  // set the column to NULL when the request left it out.
  private rows(): Record<string, unknown>[] {
    const list = Array.isArray(this.values) ? this.values : [this.values];
    return list.map((r: unknown) => {
      if (r === null || typeof r !== "object" || Array.isArray(r)) {
        throw new Error(`Supabase compatibility: ${this.op}() takes an object or an array of objects`);
      }
      return Object.fromEntries(Object.entries(r as Record<string, unknown>).filter(([, v]) => v !== undefined));
    });
  }

  // wrap makes a data-changing statement return its rows as JSON, shaped
  // by select() if one was chained.
  private wrap(statement: string): string {
    if (this.returning === null) return statement;
    return `WITH _w AS (${statement} RETURNING ${quoteIdent(this.table)}.*) SELECT coalesce(json_agg(_r), '[]'::json) AS data FROM (SELECT ${
      parseColumns(this.returning).replaceAll(quoteIdent(this.table) + ".", "")
    } FROM _w) _r`;
  }

  private built: { text: string; params: unknown[]; count?: string } | null = null;

  /** The SQL and parameters this request runs. Exposed for tests. */
  toSQL(): { text: string; params: unknown[]; count?: string } {
    // Built once: building adds the rows to the parameters.
    if (!this.built) this.built = this.build();
    return this.built;
  }

  private build(): { text: string; params: unknown[]; count?: string } {
    const t = quoteIdent(this.table);
    switch (this.op) {
      case "select": {
        let inner = `SELECT ${parseColumns(this.columns)} FROM ${t}${this.whereSQL()}`;
        if (this.orders.length) inner += " ORDER BY " + this.orders.join(", ");
        if (this.limitN !== null) inner += ` LIMIT ${this.limitN}`;
        if (this.offsetN !== null) inner += ` OFFSET ${this.offsetN}`;
        const out: { text: string; params: unknown[]; count?: string } = {
          text: `SELECT coalesce(json_agg(_r), '[]'::json) AS data FROM (${inner}) _r`,
          params: this.params,
        };
        if (this.countMode) out.count = `SELECT count(*)::int AS n FROM ${t}${this.whereSQL()}`;
        return out;
      }
      case "insert":
      case "upsert": {
        const rows = this.rows();
        if (this.where.length) throw new Error(`Supabase compatibility: ${this.op}() takes no filters`);
        const keys = [...new Set(rows.flatMap((r) => Object.keys(r)))];
        let stmt: string;
        if (keys.length === 0) {
          stmt = `INSERT INTO ${t} DEFAULT VALUES`;
        } else {
          // The rows travel as one JSON text, and the database turns each
          // value into its column's type (arrays, jsonb, timestamps), as
          // PostgREST does. The parameter is text on purpose: the driver
          // would encode a json parameter a second time.
          const cols = keys.map(quoteIdent).join(", ");
          this.params.push(JSON.stringify(rows));
          stmt = `INSERT INTO ${t} (${cols}) SELECT ${cols} FROM json_populate_recordset(null::${t}, $${this.params.length}::text::json)`;
        }
        if (this.op === "upsert") {
          let target = this.upsertOpts.onConflict;
          if (!target) {
            if (!keys.includes("id")) {
              throw new Error("Supabase compatibility: upsert() needs { onConflict: \"column\" } here (without it the rows' \"id\" column is used, and these rows have none)");
            }
            target = "id";
          }
          const conflict = target.split(",").map((c) => quoteIdent(c.trim())).join(", ");
          const updates = keys.map((k) => `${quoteIdent(k)} = EXCLUDED.${quoteIdent(k)}`).join(", ");
          stmt += this.upsertOpts.ignoreDuplicates || !updates
            ? ` ON CONFLICT (${conflict}) DO NOTHING`
            : ` ON CONFLICT (${conflict}) DO UPDATE SET ${updates}`;
        }
        return { text: this.wrap(stmt), params: this.params };
      }
      case "update": {
        const [row] = this.rows();
        const keys = Object.keys(row);
        if (keys.length === 0) throw new Error("Supabase compatibility: update() with no columns");
        if (!this.where.length) throw new Error("Supabase compatibility: update() needs a filter (eq, in, …)");
        this.params.push(JSON.stringify(row));
        const set = keys.map((k) => `${quoteIdent(k)} = _v.${quoteIdent(k)}`).join(", ");
        const stmt = `UPDATE ${t} SET ${set} FROM json_populate_record(null::${t}, $${this.params.length}::text::json) _v${this.whereSQL()}`;
        return { text: this.wrap(stmt), params: this.params };
      }
      case "delete": {
        if (!this.where.length) throw new Error("Supabase compatibility: delete() needs a filter (eq, in, …)");
        return { text: this.wrap(`DELETE FROM ${t}${this.whereSQL()}`), params: this.params };
      }
    }
  }

  private async execute(): Promise<any> {
    let data: any = null;
    let count: number | null = null;
    try {
      const q = this.toSQL();
      if (q.count) {
        const [c] = await this.run(q.count, q.params);
        count = c ? Number(c.n) : 0;
      }
      if (!(this.op === "select" && this.head)) {
        const out = await this.run(q.text, q.params);
        if (this.op === "select" || this.returning !== null) {
          data = out[0]?.data ?? [];
          if (typeof data === "string") data = JSON.parse(data);
        }
      }
    } catch (e) {
      if (this.throwing) throw e;
      return { data: null, error: toError(e), count: null, status: 400, statusText: "Bad Request" };
    }
    if (Array.isArray(data) && this.mode !== "many") {
      if (data.length === 1) {
        data = data[0];
      } else if (data.length === 0 && this.mode === "maybeSingle") {
        data = null;
      } else {
        const error: CompatError = {
          message: "JSON object requested, multiple (or no) rows returned",
          details: `The result contains ${data.length} rows`,
          hint: "",
          code: "PGRST116",
        };
        if (this.throwing) throw new Error(error.message);
        return { data: null, error, count, status: 406, statusText: "Not Acceptable" };
      }
    }
    return { data, error: null, count, status: 200, statusText: "OK" };
  }

  then<A = any, B = never>(
    onfulfilled?: ((value: any) => A | PromiseLike<A>) | null,
    onrejected?: ((reason: any) => B | PromiseLike<B>) | null,
  ): PromiseLike<A | B> {
    return this.execute().then(onfulfilled, onrejected);
  }
}

// ── rpc ─────────────────────────────────────────────────────────────

class RpcBuilder implements PromiseLike<any> {
  private mode: "many" | "single" | "maybeSingle" = "many";
  private throwing = false;
  constructor(private run: Runner, private fn: string, private args: Record<string, unknown>) {}
  single() { this.mode = "single"; return this; }
  maybeSingle() { this.mode = "maybeSingle"; return this; }
  throwOnError() { this.throwing = true; return this; }

  toSQL(): { text: string; params: unknown[] } {
    const params: unknown[] = [];
    const args = Object.entries(this.args ?? {}).map(([k, v]) => {
      params.push(toParam(v));
      return `${quoteIdent(k)} => $${params.length}`;
    });
    return { text: `SELECT to_json(_r) AS data FROM ${quoteIdent(this.fn)}(${args.join(", ")}) _r`, params };
  }

  private async execute(): Promise<any> {
    try {
      const q = this.toSQL();
      const out = await this.run(q.text, q.params);
      const values = out.map((r) => (typeof r.data === "string" && r.data !== "" ? safeParse(r.data) : r.data));
      // A function that returns one plain value gives that value; one
      // that returns rows gives the rows.
      let data: any = values;
      const scalar = values.length === 1 && (values[0] === null || typeof values[0] !== "object" || Array.isArray(values[0]));
      if (scalar || (this.mode !== "many" && values.length === 1)) data = values[0];
      else if (this.mode !== "many" && values.length === 0) data = null;
      return { data, error: null, status: 200, statusText: "OK" };
    } catch (e) {
      if (this.throwing) throw e;
      return { data: null, error: toError(e), status: 400, statusText: "Bad Request" };
    }
  }

  then<A = any, B = never>(
    onfulfilled?: ((value: any) => A | PromiseLike<A>) | null,
    onrejected?: ((reason: any) => B | PromiseLike<B>) | null,
  ): PromiseLike<A | B> {
    return this.execute().then(onfulfilled, onrejected);
  }
}

function wholeNumber(n: unknown, where: string): number {
  const v = Math.floor(Number(n));
  if (!Number.isFinite(v) || v < 0) throw new Error(`Supabase compatibility: ${where} takes a number that is zero or more`);
  return v;
}

function safeParse(s: string): unknown {
  try {
    return JSON.parse(s);
  } catch {
    return s;
  }
}

// ── auth ────────────────────────────────────────────────────────────

async function loadUser(run: Runner, id: string, email: string): Promise<any> {
  const user: any = {
    id,
    email,
    aud: "authenticated",
    role: "authenticated",
    app_metadata: {},
    user_metadata: {},
  };
  try {
    // The caller may read their own row; the rest of the profile is there.
    const [row] = await run(
      `SELECT to_json(_u) AS data FROM (SELECT email, phone, metadata, email_confirmed_at, created_at, updated_at, last_sign_in_at FROM users WHERE id = $1) _u`,
      [id],
    );
    const u = row && (typeof row.data === "string" ? safeParse(row.data) : row.data) as any;
    if (u) {
      const { _app_metadata, ...meta } = u.metadata ?? {};
      user.email = u.email ?? email;
      user.phone = u.phone ?? "";
      user.user_metadata = meta;
      user.app_metadata = _app_metadata ?? {};
      user.email_confirmed_at = u.email_confirmed_at;
      user.created_at = u.created_at;
      user.updated_at = u.updated_at;
      user.last_sign_in_at = u.last_sign_in_at;
    }
  } catch {
    // The id and email from the verified token are enough to go on.
  }
  return user;
}

function authFor(run: Runner, service: Runner) {
  const missing = { message: "Auth session missing!", name: "AuthSessionMissingError", status: 401, code: "session_not_found" };
  const noAdmin = (name: string) => () =>
    unsupported(`auth.admin.${name}()`, "manage users with the users API from your server, or with SQL on the users table through ctx.db.asService()");
  return {
    // The platform has already verified the caller's token; the token
    // passed here is not looked at.
    async getUser(_jwt?: string) {
      const u = current().user;
      if (!u) return { data: { user: null }, error: missing };
      return { data: { user: await loadUser(run, u.id, u.email) }, error: null };
    },
    async getSession() {
      const u = current().user;
      if (!u) return { data: { session: null }, error: null };
      return { data: { session: { user: await loadUser(run, u.id, u.email), access_token: "", token_type: "bearer" } }, error: null };
    },
    admin: {
      async getUserById(id: string) {
        try {
          const [row] = await service(`SELECT email FROM users WHERE id = $1`, [id]);
          if (!row) return { data: { user: null }, error: { message: "User not found", status: 404, code: "user_not_found" } };
          return { data: { user: await loadUser(service, id, row.email) }, error: null };
        } catch (e) {
          return { data: { user: null }, error: toError(e) };
        }
      },
      listUsers: noAdmin("listUsers"),
      createUser: noAdmin("createUser"),
      updateUserById: noAdmin("updateUserById"),
      deleteUser: noAdmin("deleteUser"),
      inviteUserByEmail: noAdmin("inviteUserByEmail"),
      generateLink: noAdmin("generateLink"),
    },
    signInWithPassword: () => unsupported("auth.signInWithPassword()", "sign users in from your app with the Eurobase SDK"),
    signUp: () => unsupported("auth.signUp()", "sign users up from your app with the Eurobase SDK"),
    signOut: () => Promise.resolve({ error: null }),
    setSession: () => Promise.resolve({ data: { session: null, user: null }, error: null }),
  };
}

// ── storage ─────────────────────────────────────────────────────────

function storageFor() {
  const no = (name: string, instead: string) => () => unsupported(`storage.${name}()`, instead);
  return {
    // A project has one bucket; a Supabase bucket becomes a top folder.
    from(bucket: string) {
      const key = (path: string) => `${bucket}/${String(path).replace(/^\/+/, "")}`;
      return {
        async upload(path: string, body: unknown, opts?: { contentType?: string }) {
          try {
            await current().storage.upload(key(path), body, { contentType: opts?.contentType });
            return { data: { path, fullPath: key(path) }, error: null };
          } catch (e) {
            return { data: null, error: toError(e) };
          }
        },
        async createSignedUrl(path: string, expiresIn: number) {
          try {
            const r = await current().storage.createSignedUrl(key(path), "download", { expiresIn });
            return { data: { signedUrl: r.url }, error: null };
          } catch (e) {
            return { data: null, error: toError(e) };
          }
        },
        async createSignedUploadUrl(path: string) {
          try {
            const r = await current().storage.createSignedUrl(key(path), "upload", {});
            return { data: { signedUrl: r.url, path, token: "" }, error: null };
          } catch (e) {
            return { data: null, error: toError(e) };
          }
        },
        async remove(paths: string[]) {
          try {
            for (const p of paths) await current().storage.delete(key(p));
            return { data: paths.map((name) => ({ name })), error: null };
          } catch (e) {
            return { data: null, error: toError(e) };
          }
        },
        download: no("download", "create a signed URL and fetch it"),
        list: no("list", "read the storage_objects table with ctx.db.sql"),
        getPublicUrl: no("getPublicUrl", "share the folder in the console (Storage → Sharing) and build the URL, or create a signed URL"),
        move: no("move", "upload under the new name and remove the old file"),
        copy: no("copy", "upload the file again under the new name"),
        update: no("update", "upload the file again"),
      };
    },
    listBuckets: no("listBuckets", "a Eurobase project has one bucket"),
    createBucket: no("createBucket", "a Eurobase project has one bucket; use a folder"),
    getBucket: no("getBucket", "a Eurobase project has one bucket"),
  };
}

// ── client ──────────────────────────────────────────────────────────

export function createClient(_url?: string, key?: string, _options?: unknown) {
  // Any key but the service role key means "as the caller". On Supabase a
  // call without a signed-in user would then be the anon role, with only
  // what RLS gives everyone. A Eurobase function's SQL has no anon role:
  // without a user it runs with the service role. So this client refuses
  // to run SQL when nobody is signed in, instead of quietly doing more
  // than the code asked for.
  const asCaller: Runner = (q, p) => {
    const c = current();
    if (!c.user) {
      return Promise.reject(
        new Error(
          "Supabase compatibility: this client was made without the service role key and nobody is signed in — on Eurobase such a query would run with the service role, so it is refused. Require sign-in for this function, or use a client made with SUPABASE_SERVICE_ROLE_KEY for what it does on its own behalf",
        ),
      );
    }
    return c.db.sql(q, p);
  };
  // Without a signed-in caller the function is the service role already.
  const asService: Runner = (q, p) => {
    const c = current();
    return c.user ? c.db.asService().sql(q, p) : c.db.sql(q, p);
  };
  const run = isServiceRoleKey(key) ? asService : asCaller;
  return {
    auth: authFor(asCaller, asService),
    from: (table: string) => new QueryBuilder(run, table),
    rpc: (fn: string, args: Record<string, unknown> = {}) => new RpcBuilder(run, fn, args),
    storage: storageFor(),
    schema: (name: string) => unsupported(`schema(${JSON.stringify(name)})`, "a project's tables are in one schema; drop the call"),
    functions: {
      invoke: (name: string) =>
        unsupported(`functions.invoke(${JSON.stringify(name)})`, "a function can't call the project's own API; move the shared code into a file both functions import"),
    },
    channel: (name: string) => unsupported(`channel(${JSON.stringify(name)})`, "realtime is used from your app, not from a function"),
    removeChannel: () => Promise.resolve("ok"),
    removeAllChannels: () => Promise.resolve([]),
  };
}

export type SupabaseClient = ReturnType<typeof createClient>;
export default { createClient };
