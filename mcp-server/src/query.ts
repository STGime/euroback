/** Query-string building for queryTable (the platform data API). Pure, for tests. */

export interface QueryTableInput {
  projectId: string;
  table: string;
  columns?: string[];
  filters?: { column: string; op: string; value: string }[];
  orderBy?: { column: string; descending?: boolean }[];
  limit?: number;
  offset?: number;
  aggregate?: { fn: string; column?: string };
}

/** Query parameters the data API reads itself: a filter on a column of that
 *  name can't be expressed (it would be dropped or misread), so refuse it. */
const RESERVED_PARAMS = new Set(['select', 'order', 'limit', 'offset', 'aggregate']);

/** One URL path segment from a name (table, function, secret): refused if
 *  empty, "." / "..", or containing "/" — so a model-supplied name can't
 *  walk to another API route. */
export function pathSegment(name: string): string {
  if (!name || name === '.' || name === '..' || name.includes('/') || name.includes('\\')) {
    throw new Error(`invalid name: ${JSON.stringify(name)}`);
  }
  return encodeURIComponent(name);
}

/** `?select=…&col=op.value&order=…&limit=…&offset=…&aggregate=…` for GET /data/{table}. */
export function buildQueryString(input: QueryTableInput): string {
  for (const f of input.filters ?? []) {
    if (RESERVED_PARAMS.has(f.column)) {
      throw new Error(`can't filter on a column named "${f.column}" with queryTable (it clashes with a query parameter); use runSQL`);
    }
  }
  const q = new URLSearchParams();
  if (input.columns?.length) q.set('select', input.columns.join(','));
  for (const f of input.filters ?? []) {
    const value = f.op === 'in' ? `(${f.value})` : f.value;
    q.append(f.column, `${f.op}.${value}`);
  }
  if (input.orderBy?.length) {
    q.set('order', input.orderBy.map((o) => `${o.column}.${o.descending ? 'desc' : 'asc'}`).join(','));
  }
  if (input.limit !== undefined) q.set('limit', String(input.limit));
  if (input.offset !== undefined) q.set('offset', String(input.offset));
  if (input.aggregate) {
    q.set('aggregate', input.aggregate.column ? `${input.aggregate.fn}:${input.aggregate.column}` : input.aggregate.fn);
  }
  const s = q.toString();
  return s ? `?${s}` : '';
}

/**
 * The platform's own tables in every tenant schema (mirror of
 * internal/query/internal_tables.go): end users, their identities, auth
 * tokens, the vault, storage bookkeeping. queryTable is marked read-only,
 * so clients run it without asking; these rows (end users' personal data)
 * must only reach the model through a tool the user approves — listUsers,
 * or runSQL.
 */
const INTERNAL_TABLES = new Set([
  'users',
  'user_identities',
  'refresh_tokens',
  'email_tokens',
  'vault_secrets',
  'storage_objects',
  'storage_shared_prefixes',
]);

/** Whether queryTable must refuse this read: an internal table, directly
 *  or embedded in the column list (`owner:users(email)`, `users!inner(*)`). */
export function readsInternalTable(table: string, columns: string[] = []): string | undefined {
  if (INTERNAL_TABLES.has(table)) return table;
  for (const c of columns) {
    for (const m of c.matchAll(/([A-Za-z_][A-Za-z0-9_]*)\s*(?:![A-Za-z_][A-Za-z0-9_]*\s*)?\(/g)) {
      if (INTERNAL_TABLES.has(m[1])) return m[1];
    }
  }
  return undefined;
}

/**
 * Compliance export archives (exports/<project-id>/…, #655) are full
 * project dumps: the MCP tools never read them or sign links to them —
 * download them from the console (Compliance → Data Export).
 */
export function isExportArchiveKey(key: string): boolean {
  return key.replace(/^\/+/, '').toLowerCase().startsWith('exports/');
}

/** Path of a storage key under /storage/, each segment encoded. Empty,
 *  "." and ".." segments are refused: fetch would resolve them before the
 *  gateway's own key check, reaching other API routes. */
export function storageKeyPath(key: string): string {
  const segs = key.replace(/^\/+/, '').split('/');
  if (segs.length === 0 || segs.some((s) => s === '' || s === '.' || s === '..' || s.includes('\\'))) {
    throw new Error(`invalid storage key: ${JSON.stringify(key)}`);
  }
  return segs.map((seg) => encodeURIComponent(seg)).join('/');
}

/** How downloadFile returns a file: images as images, text as text, else metadata only. */
export function downloadKind(contentType: string): 'image' | 'text' | 'binary' {
  const ct = contentType.toLowerCase().split(';')[0].trim();
  // Only formats MCP clients / the model accept as images.
  if (['image/png', 'image/jpeg', 'image/gif', 'image/webp'].includes(ct)) return 'image';
  if (ct.startsWith('text/') || ['application/json', 'application/xml', 'application/javascript', 'application/x-ndjson', 'image/svg+xml', 'application/csv'].includes(ct)) {
    return 'text';
  }
  return 'binary';
}
