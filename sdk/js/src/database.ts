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
   *   ?order=created_at.desc
   *   ?limit=20&offset=0
   */
  private buildParams(): Record<string, string> {
    const params: Record<string, string> = {}

    if (this.columns.length > 0) {
      params['select'] = this.columns.join(',')
    }

    for (const f of this.filters) {
      params[f.column] = `${f.operator}.${f.value}`
    }

    if (this.orders.length > 0) {
      params['order'] = this.orders
        .map(o => `${o.column}.${o.descending ? 'desc' : 'asc'}`)
        .join(',')
    }

    if (this.limitCount !== null) {
      params['limit'] = String(this.limitCount)
    }

    if (this.offsetCount !== null) {
      params['offset'] = String(this.offsetCount)
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

  constructor(config: EurobaseConfig) {
    this.http = httpClient(config)
    this.schema = new SchemaClient(config)
  }

  /** Start a query chain for the given table. */
  from<T = Record<string, any>>(table: string): QueryBuilder<T> {
    return new QueryBuilder<T>(this.http, table)
  }
}
