/**
 * Schema DDL client — create, alter, and drop tables in the caller's tenant
 * schema. Requires a secret API key (eb_sk_*). The gateway rejects public
 * keys with 403.
 *
 * RLS is ON by default for new tables. To create a public table with RLS
 * off, pass { disableRLS: true } on createTable(); the response will
 * include a top-level `warning` so you see what you just did.
 */

import type { EurobaseConfig } from './http'

export interface ColumnDefinition {
  name: string
  type: string
  nullable?: boolean
  default_value?: string
  primary_key?: boolean
}

export type RLSPreset =
  | 'owner_access'
  | 'public_read_owner_write'
  | 'authenticated_read_owner_write'
  | 'full_access'
  | 'read_only'
  | 'none'

export interface CreateTableOptions {
  /** Columns to include in the new table. Order matters for display. */
  columns: ColumnDefinition[]
  /**
   * RLS policy preset to apply after table creation. If omitted, the
   * gateway auto-detects an owner-style column (user_id, owner_id, created_by)
   * and applies "owner_access". Pass "none" to skip the auto-preset.
   */
  rlsPreset?: RLSPreset
  /** Column to use as the owner identifier for the preset. */
  rlsUserIdColumn?: string
  /**
   * Disable RLS entirely. Only use for genuinely public data — every
   * authenticated request can read/write every row. The response will
   * include a `warning` field when set.
   */
  disableRLS?: boolean
}

export interface CreateTableResult {
  status: 'created'
  table: string
  rls_enabled: boolean
  rls_preset: string
  /** Present when disableRLS was set. A plain-language description of the risk. */
  warning?: string
}

export class SchemaClient {
  private config: EurobaseConfig

  constructor(config: EurobaseConfig) {
    this.config = config
  }

  private get baseUrl(): string {
    return this.config.url.replace(/\/+$/, '')
  }

  private headers(): Record<string, string> {
    return {
      apikey: this.config.apiKey,
      'Content-Type': 'application/json',
    }
  }

  /**
   * Create a new table in the tenant schema. Requires a secret API key.
   *
   * @example
   * ```ts
   * const { data, error } = await eb.db.schema.createTable('posts', {
   *   columns: [
   *     { name: 'id', type: 'uuid', primary_key: true, default_value: 'gen_random_uuid()' },
   *     { name: 'title', type: 'text' },
   *     { name: 'body', type: 'text', nullable: true },
   *     { name: 'user_id', type: 'uuid' }, // auto-detected as owner → owner_access preset
   *   ],
   * })
   * ```
   */
  async createTable(
    name: string,
    opts: CreateTableOptions,
  ): Promise<{ data: CreateTableResult | null; error: string | null }> {
    try {
      const body: Record<string, unknown> = { name, columns: opts.columns }
      if (opts.rlsPreset) body.rls_preset = opts.rlsPreset
      if (opts.rlsUserIdColumn) body.rls_user_id_column = opts.rlsUserIdColumn
      if (opts.disableRLS) body.disable_rls = true

      const res = await fetch(`${this.baseUrl}/v1/db/schema/tables/`, {
        method: 'POST',
        headers: this.headers(),
        body: JSON.stringify(body),
      })
      if (!res.ok) {
        const errBody = await res.json().catch(() => ({}))
        return { data: null, error: errBody?.error || `HTTP ${res.status}` }
      }
      const data: CreateTableResult = await res.json()
      return { data, error: null }
    } catch (err) {
      return { data: null, error: (err as Error).message }
    }
  }

  /** Drop a table from the tenant schema. Irreversible. */
  async dropTable(name: string): Promise<{ error: string | null }> {
    try {
      const res = await fetch(`${this.baseUrl}/v1/db/schema/tables/${encodeURIComponent(name)}`, {
        method: 'DELETE',
        headers: this.headers(),
      })
      if (!res.ok) {
        const errBody = await res.json().catch(() => ({}))
        return { error: errBody?.error || `HTTP ${res.status}` }
      }
      return { error: null }
    } catch (err) {
      return { error: (err as Error).message }
    }
  }

  /** Add a column to an existing table. */
  async addColumn(
    table: string,
    column: ColumnDefinition,
  ): Promise<{ error: string | null }> {
    try {
      const res = await fetch(
        `${this.baseUrl}/v1/db/schema/tables/${encodeURIComponent(table)}/columns`,
        {
          method: 'POST',
          headers: this.headers(),
          body: JSON.stringify({
            name: column.name,
            type: column.type,
            nullable: column.nullable ?? false,
            default_value: column.default_value ?? '',
          }),
        },
      )
      if (!res.ok) {
        const errBody = await res.json().catch(() => ({}))
        return { error: errBody?.error || `HTTP ${res.status}` }
      }
      return { error: null }
    } catch (err) {
      return { error: (err as Error).message }
    }
  }

  /** Drop a column from an existing table. Irreversible. */
  async dropColumn(table: string, column: string): Promise<{ error: string | null }> {
    try {
      const res = await fetch(
        `${this.baseUrl}/v1/db/schema/tables/${encodeURIComponent(table)}/columns/${encodeURIComponent(column)}`,
        { method: 'DELETE', headers: this.headers() },
      )
      if (!res.ok) {
        const errBody = await res.json().catch(() => ({}))
        return { error: errBody?.error || `HTTP ${res.status}` }
      }
      return { error: null }
    } catch (err) {
      return { error: (err as Error).message }
    }
  }
}
