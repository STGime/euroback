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

/** `?select=…&col=op.value&order=…&limit=…&offset=…&aggregate=…` for GET /data/{table}. */
export function buildQueryString(input: QueryTableInput): string {
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

/** Path of a storage key under /storage/, each segment encoded. */
export function storageKeyPath(key: string): string {
  return key
    .replace(/^\/+/, '')
    .split('/')
    .map((seg) => encodeURIComponent(seg))
    .join('/');
}

/** How downloadFile returns a file: images as images, text as text, else metadata only. */
export function downloadKind(contentType: string): 'image' | 'text' | 'binary' {
  const ct = contentType.toLowerCase().split(';')[0].trim();
  if (ct.startsWith('image/') && ct !== 'image/svg+xml') return 'image';
  if (ct.startsWith('text/') || ['application/json', 'application/xml', 'application/javascript', 'application/x-ndjson', 'image/svg+xml', 'application/csv'].includes(ct)) {
    return 'text';
  }
  return 'binary';
}
