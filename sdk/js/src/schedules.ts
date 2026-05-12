/**
 * Schedules client — scheduled function triggers (cron jobs).
 *
 * Closes #112. Exposed as `eb.functions.schedules` so a deployed edge
 * function can declare its own cron alongside the deploy:
 *
 * ```ts
 * await eb.functions.schedules.create('purge-expired-images', {
 *   functionName: 'purge-expired-images',
 *   cron: '0 4 * * *',
 *   timezone: 'UTC',
 *   description: 'Daily purge of session_images past 24h TTL',
 * })
 * ```
 *
 * Requires an `eb_sk_*` secret key — public keys live in client code and
 * must not be able to install or remove a schedule.
 *
 * Semantics (locked-in defaults; see issue #112):
 *   - `create()` is NOT an upsert. Calling it twice with the same name
 *     returns `already_exists` (HTTP 409). Use `update()` to change an
 *     existing schedule. A separate `createOrUpdate` helper is provided
 *     for provisioning scripts that don't want to branch on the error.
 *   - Missed ticks during platform downtime are dropped (no backfill).
 *   - Each tick is independent — no overlap protection in v1.
 *   - Cron grammar is POSIX 5-field (minute hour day-of-month month
 *     day-of-week), evaluated in the schedule's `timezone` (defaults to
 *     UTC). `@daily` / `@hourly` aliases are not supported.
 */

import type { HttpClient } from './http'

export interface ScheduleSpec {
  /** Function to invoke. Must already be deployed. */
  functionName: string
  /** Standard 5-field cron expression, evaluated in `timezone`. */
  cron: string
  /** Optional IANA timezone. Defaults to UTC. */
  timezone?: string
  /** Optional JSON body passed to the function on each tick. */
  payload?: unknown
  /** Optional headers added to the function invocation. */
  headers?: Record<string, string>
  /** Optional human-readable label that surfaces in the dashboard. */
  description?: string
  /** Whether the schedule is currently active. Defaults to true. */
  enabled?: boolean
}

export interface ScheduleRow {
  id: string
  projectId: string
  name: string
  functionName: string
  cron: string
  timezone: string
  payload?: unknown
  headers?: Record<string, string>
  description?: string | null
  enabled: boolean
  lastRunAt: string | null
  lastError: string | null
  runCount: number
  createdAt: string
  updatedAt: string
}

/** Discriminated error so callers can distinguish "name collision" from
 *  validation / auth failures without parsing strings. */
export interface ScheduleError {
  status: number
  message: string
  /** `already_exists` is set on create() when the name is in use. */
  code?: 'already_exists' | 'not_found' | 'forbidden' | 'invalid' | 'unknown'
}

interface ServerScheduleRow {
  id: string
  project_id: string
  name: string
  schedule: string
  timezone: string
  action_type: string
  action: string
  description: string | null
  payload?: unknown
  headers?: Record<string, string>
  enabled: boolean
  last_run_at: string | null
  last_error: string | null
  run_count: number
  created_at: string
  updated_at: string
}

function fromServer(row: ServerScheduleRow): ScheduleRow {
  return {
    id: row.id,
    projectId: row.project_id,
    name: row.name,
    // action_type is always 'function' for schedules created via this
    // client; legacy sql/rpc schedules created via the platform UI
    // surface their action verbatim so callers can at least see them.
    functionName: row.action,
    cron: row.schedule,
    timezone: row.timezone,
    payload: row.payload,
    headers: row.headers,
    description: row.description,
    enabled: row.enabled,
    lastRunAt: row.last_run_at,
    lastError: row.last_error,
    runCount: row.run_count,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
  }
}

/** Build the create/update payload the gateway expects (action_type
 *  defaults to 'function' since that's the only thing the SDK creates). */
function toServerCreate(name: string, spec: ScheduleSpec) {
  return {
    name,
    schedule: spec.cron,
    timezone: spec.timezone,
    action_type: 'function',
    action: spec.functionName,
    description: spec.description,
    payload: spec.payload,
    headers: spec.headers,
    enabled: spec.enabled,
  }
}

function toServerUpdate(spec: Partial<ScheduleSpec>) {
  const body: Record<string, unknown> = {}
  if (spec.cron !== undefined) body.schedule = spec.cron
  if (spec.timezone !== undefined) body.timezone = spec.timezone
  if (spec.functionName !== undefined) {
    body.action_type = 'function'
    body.action = spec.functionName
  }
  if (spec.description !== undefined) body.description = spec.description
  if (spec.payload !== undefined) body.payload = spec.payload
  if (spec.headers !== undefined) body.headers = spec.headers
  if (spec.enabled !== undefined) body.enabled = spec.enabled
  return body
}

function classifyError(result: any): ScheduleError {
  const msg = typeof result?.error === 'string' ? result.error : 'request failed'
  // Status code is embedded in the message by http.ts when the response
  // is not JSON (`HTTP 409: Conflict`). Best-effort parse — the server
  // also sends JSON-shaped {"error":"…"} bodies which lose the status.
  const m = msg.match(/^HTTP (\d+):/)
  const status = m ? Number(m[1]) : 0
  let code: ScheduleError['code'] = 'unknown'
  if (status === 409 || /already exists/i.test(msg)) code = 'already_exists'
  else if (status === 404 || /not found/i.test(msg)) code = 'not_found'
  else if (status === 401 || status === 403) code = 'forbidden'
  else if (status === 400) code = 'invalid'
  return { status, message: msg, code }
}

export class SchedulesClient {
  private http: HttpClient

  constructor(http: HttpClient) {
    this.http = http
  }

  /**
   * Create a new schedule. Returns `{ error: { code: 'already_exists' } }`
   * if a schedule with this name is already configured — use `update()`
   * to change an existing schedule, or `createOrUpdate()` for idempotent
   * provisioning.
   */
  async create(
    name: string,
    spec: ScheduleSpec,
  ): Promise<{ data: ScheduleRow | null; error: ScheduleError | null }> {
    const result = await this.http.post('/v1/schedules', toServerCreate(name, spec))
    if (result?.error) {
      return { data: null, error: classifyError(result) }
    }
    return { data: fromServer(result as ServerScheduleRow), error: null }
  }

  /** Update an existing schedule by name. */
  async update(
    name: string,
    spec: Partial<ScheduleSpec>,
  ): Promise<{ data: ScheduleRow | null; error: ScheduleError | null }> {
    const result = await this.http.patch(
      `/v1/schedules/${encodeURIComponent(name)}`,
      toServerUpdate(spec),
    )
    if (result?.error) {
      return { data: null, error: classifyError(result) }
    }
    return { data: fromServer(result as ServerScheduleRow), error: null }
  }

  /** Get a schedule by name. */
  async get(
    name: string,
  ): Promise<{ data: ScheduleRow | null; error: ScheduleError | null }> {
    const result = await this.http.get(`/v1/schedules/${encodeURIComponent(name)}`)
    if (result?.error) {
      return { data: null, error: classifyError(result) }
    }
    return { data: fromServer(result as ServerScheduleRow), error: null }
  }

  /** List all schedules for the project. */
  async list(): Promise<{ data: ScheduleRow[]; error: ScheduleError | null }> {
    const result = await this.http.get('/v1/schedules')
    if (result?.error) {
      return { data: [], error: classifyError(result) }
    }
    return { data: (result as ServerScheduleRow[]).map(fromServer), error: null }
  }

  /** Delete a schedule by name. */
  async delete(name: string): Promise<{ error: ScheduleError | null }> {
    const result = await this.http.del(`/v1/schedules/${encodeURIComponent(name)}`)
    if (result?.error) {
      return { error: classifyError(result) }
    }
    return { error: null }
  }

  /**
   * Idempotent: create the schedule if missing, update it if present.
   * Useful for provisioning scripts that want a single declaration.
   */
  async createOrUpdate(
    name: string,
    spec: ScheduleSpec,
  ): Promise<{ data: ScheduleRow | null; error: ScheduleError | null }> {
    const created = await this.create(name, spec)
    if (!created.error) return created
    if (created.error.code === 'already_exists') {
      return this.update(name, spec)
    }
    return created
  }
}
