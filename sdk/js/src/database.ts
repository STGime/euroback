/**
 * Database client with a fluent query builder that mirrors the
 * PostgREST/Supabase query param format used by the Eurobase gateway.
 */

import type { EurobaseConfig } from './http'
import { httpClient, type HttpClient } from './http'
import { SchemaClient } from './ddl'

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

/** Result envelope returned by all query operations. */
export interface QueryResult<T = Record<string, any>> {
  data: T | T[] | null
  count: number | null
  error: string | null
}

/** Result of `db.rpc()`. */
export interface RpcResult<T = any> {
  /** What the function returned, or `null` on an error. */
  data: T | null
  error: string | null
  /**
   * The Postgres error code when the database refused the call: `P0001`
   * for the function's own `RAISE EXCEPTION`, `42501` for a permission or
   * RLS denial, … `null` otherwise (success, network error, not found).
   */
  code: string | null
  /** HTTP status of an error response; absent on success or with no response. */
  status?: number
}

// ---------------------------------------------------------------------------
// Internal filter / order types
// ---------------------------------------------------------------------------

interface Filter {
  column: string
  operator: string
  value: string
}

interface OrderClause {
  column: string
  descending: boolean
}

// ---------------------------------------------------------------------------
// QueryBuilder
// ---------------------------------------------------------------------------

/**
 * Chainable query builder. Call terminal methods (then / insert / update /
 * delete) to execute the query against the gateway.
 */
export class QueryBuilder<T = Record<string, any>> implements PromiseLike<QueryResult<T>> {
  private http: HttpClient
  private table: string
  private columns: string[] = []
  private filters: Filter[] = []
  private orders: OrderClause[] = []
  private limitCount: number | null = null
  private offsetCount: number | null = null
  private expectSingle = false

  constructor(http: HttpClient, table: string) {
    this.http = http
    this.table = table
  }

  // ---- Column selection ---------------------------------------------------

  /** Select specific columns to return. */
  select(...columns: string[]): QueryBuilder<T> {
    this.columns.push(...columns)
    return this
  }

  // ---- Filters ------------------------------------------------------------

  /** Equals filter: `column = value` */
  eq(column: string, value: any): QueryBuilder<T> {
    this.filters.push({ column, operator: 'eq', value: String(value) })
    return this
  }

  /** Not-equals filter: `column != value` */
  neq(column: string, value: any): QueryBuilder<T> {
    this.filters.push({ column, operator: 'neq', value: String(value) })
    return this
  }

  /** Greater-than filter: `column > value` */
  gt(column: string, value: any): QueryBuilder<T> {
    this.filters.push({ column, operator: 'gt', value: String(value) })
    return this
  }

  /** Greater-than-or-equal filter: `column >= value` */
  gte(column: string, value: any): QueryBuilder<T> {
    this.filters.push({ column, operator: 'gte', value: String(value) })
    return this
  }

  /** Less-than filter: `column < value` */
  lt(column: string, value: any): QueryBuilder<T> {
    this.filters.push({ column, operator: 'lt', value: String(value) })
    return this
  }

  /** Less-than-or-equal filter: `column <= value` */
  lte(column: string, value: any): QueryBuilder<T> {
    this.filters.push({ column, operator: 'lte', value: String(value) })
    return this
  }

  /** LIKE pattern match (case-sensitive). */
  like(column: string, pattern: string): QueryBuilder<T> {
    this.filters.push({ column, operator: 'like', value: pattern })
    return this
  }

  /** ILIKE pattern match (case-insensitive). */
  ilike(column: string, pattern: string): QueryBuilder<T> {
    this.filters.push({ column, operator: 'ilike', value: pattern })
    return this
  }

  /** IN filter: `column IN (values)` */
  in(column: string, values: any[]): QueryBuilder<T> {
    const joined = values.map(String).join(',')
    this.filters.push({ column, operator: 'in', value: `(${joined})` })
    return this
  }

  // ---- Ordering -----------------------------------------------------------

  /** Add an ORDER BY clause. Ascending by default. */
  order(column: string, opts?: { ascending?: boolean }): QueryBuilder<T> {
    const descending = opts?.ascending === false
    this.orders.push({ column, descending })
    return this
  }

  // ---- Pagination ---------------------------------------------------------

  /** Limit the number of rows returned. */
  limit(count: number): QueryBuilder<T> {
    this.limitCount = count
    return this
  }

  /** Offset for pagination. */
  offset(count: number): QueryBuilder<T> {
    this.offsetCount = count
    return this
  }

  // ---- Modifiers ----------------------------------------------------------

  /** Expect exactly one result. Unwraps the array to a single object. */
  single(): QueryBuilder<T> {
    this.expectSingle = true
    this.limitCount = 1
    return this
  }

  // ---- Terminal methods ---------------------------------------------------

  /**
   * Implements PromiseLike so the query builder can be awaited directly.
   * Executes a GET /v1/db/{table} with the accumulated query parameters.
   */
  then<TResult1 = QueryResult<T>, TResult2 = never>(
    onfulfilled?: ((value: QueryResult<T>) => TResult1 | PromiseLike<TResult1>) | null,
    onrejected?: ((reason: any) => TResult2 | PromiseLike<TResult2>) | null,
  ): Promise<TResult1 | TResult2> {
    return this.execute().then(onfulfilled, onrejected)
  }

  /** Execute the SELECT query. */
  async execute(): Promise<QueryResult<T>> {
    const params = this.buildParams()
    const res = await this.http.get(`/v1/db/${encodeURIComponent(this.table)}`, params)

    if (res.error) {
      return { data: null, count: null, error: res.error }
    }

    const data = res.data ?? null
    const count: number | null = res.count ?? null

    if (this.expectSingle) {
      if (Array.isArray(data) && data.length > 0) {
        return { data: data[0] as T, count, error: null }
      }
      return { data: null, count, error: 'Row not found' }
    }

    return { data, count, error: null }
  }

  /** Insert a new row. POST /v1/db/{table} */
  async insert(data: Record<string, any>): Promise<QueryResult<T>> {
    const res = await this.http.post(`/v1/db/${encodeURIComponent(this.table)}`, data)

    if (res.error) {
      return { data: null, count: null, error: res.error }
    }

    return { data: res as T, count: null, error: null }
  }

  /** Update a row by ID. PATCH /v1/db/{table}/{id} */
  async update(id: string, data: Record<string, any>): Promise<QueryResult<T>> {
    const res = await this.http.patch(
      `/v1/db/${encodeURIComponent(this.table)}/${encodeURIComponent(id)}`,
      data,
    )

    if (res.error) {
      return { data: null, count: null, error: res.error }
    }

    return { data: res as T, count: null, error: null }
  }

  /** Delete a row by ID. DELETE /v1/db/{table}/{id} */
  async delete(id: string): Promise<{ error: string | null }> {
    const res = await this.http.del(
      `/v1/db/${encodeURIComponent(this.table)}/${encodeURIComponent(id)}`,
    )
    return { error: res.error ?? null }
  }

  // ---- Internal -----------------------------------------------------------

  /**
   * Build URL query parameters matching the gateway's PostgREST-style format:
   *   ?select=id,name
   *   ?name=eq.Stefan
   *   ?created_at=gte.2026-01-01&created_at=lte.2026-01-31
   *   ?order=created_at.desc
   *   ?limit=20&offset=0
   *
   * Returned as [key, value] pairs, not an object: a column can carry
   * several filters (a range), and keying by column name made the last
   * one silently overwrite the others.
   */
  private buildParams(): Array<[string, string]> {
    const params: Array<[string, string]> = []

    if (this.columns.length > 0) {
      params.push(['select', this.columns.join(',')])
    }

    for (const f of this.filters) {
      params.push([f.column, `${f.operator}.${f.value}`])
    }

    if (this.orders.length > 0) {
      params.push([
        'order',
        this.orders.map(o => `${o.column}.${o.descending ? 'desc' : 'asc'}`).join(','),
      ])
    }

    if (this.limitCount !== null) {
      params.push(['limit', String(this.limitCount)])
    }

    if (this.offsetCount !== null) {
      params.push(['offset', String(this.offsetCount)])
    }

    return params
  }
}

// ---------------------------------------------------------------------------
// DatabaseClient
// ---------------------------------------------------------------------------

/** Top-level database client. Start a query chain with `.from(table)`. */
export class DatabaseClient {
  private http: HttpClient
  /**
   * Schema DDL surface — create/drop tables and columns. Requires a
   * secret API key. Tables created through here live in the caller's
   * tenant schema only; you cannot reach platform tables.
   */
  readonly schema: SchemaClient

  constructor(config: EurobaseConfig, http?: HttpClient) {
    this.http = http ?? httpClient(config)
    this.schema = new SchemaClient(config)
  }

  /** Start a query chain for the given table. */
  from<T = Record<string, any>>(table: string): QueryBuilder<T> {
    return new QueryBuilder<T>(this.http, table)
  }

  /**
   * Call a database (RPC) function. POST /v1/db/rpc/{fn}
   *
   * Arguments are passed by name, so the keys must match the function's
   * parameter names. The call runs as the signed-in user, under RLS.
   *
   * ```ts
   * const { data, error, code } = await eb.db.rpc('mission_answers', { mission_id: id })
   * if (code === 'P0001') showMessage(error) // the function's own RAISE EXCEPTION
   * ```
   */
  async rpc<T = any>(fn: string, args?: Record<string, any>): Promise<RpcResult<T>> {
    const res = await this.http.post(`/v1/db/rpc/${encodeURIComponent(fn)}`, args ?? {})
    if (res?.error) {
      const out: RpcResult<T> = {
        data: null,
        error: res.error,
        code: typeof res.code === 'string' ? res.code : null,
      }
      if (typeof res.status === 'number') out.status = res.status
      return out
    }
    return { data: (res?.result ?? null) as T | null, error: null, code: null }
  }
}
