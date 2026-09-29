/**
 * HTTP client wrapping the Eurobase platform API.
 * Forwards the caller's token (console JWT or personal access token). The
 * MCP server has no access of its own: the gateway decides what the token
 * may do.
 */

const PLATFORM_URL = process.env.EUROBASE_PLATFORM_URL || 'https://api.eurobase.app';

/** What the token may do, as reported by GET /platform/auth/account/profile (#702). */
export interface TokenScope {
  /** A project-scoped token: one project, one role. */
  scoped: boolean;
  projectId?: string;
  role?: 'viewer' | 'developer' | 'admin';
}

export class ApiError extends Error {
  constructor(message: string, readonly status: number, readonly code?: string) {
    super(message);
  }
}

export interface RawResponse {
  contentType: string;
  bytes: Uint8Array;
  truncated: boolean;
}

export class ApiClient {
  private token: string;
  /** Set by validateToken(). */
  scope: TokenScope = { scoped: false };

  constructor(token: string) {
    this.token = token;
  }

  private async fetchRaw(method: string, path: string, body?: unknown): Promise<Response> {
    const res = await fetch(`${PLATFORM_URL}${path}`, {
      method,
      headers: {
        'Authorization': `Bearer ${this.token}`,
        'Content-Type': 'application/json',
      },
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
    if (!res.ok) {
      const text = await res.text();
      let message = text;
      let code: string | undefined;
      try {
        const json = JSON.parse(text);
        message = json.error || json.message || text;
        code = json.code;
      } catch {
        // not JSON
      }
      throw new ApiError(explain(`API ${method} ${path} failed (${res.status}): ${message}`, res.status, code, this.scope, path), res.status, code);
    }
    return res;
  }

  private async request(method: string, path: string, body?: unknown): Promise<unknown> {
    const res = await this.fetchRaw(method, path, body);
    const contentType = res.headers.get('content-type') || '';
    if (contentType.includes('application/json')) {
      return res.json();
    }
    return res.text();
  }

  async get(path: string): Promise<unknown> {
    return this.request('GET', path);
  }

  async post(path: string, body?: unknown): Promise<unknown> {
    return this.request('POST', path, body);
  }

  async patch(path: string, body?: unknown): Promise<unknown> {
    return this.request('PATCH', path, body);
  }

  async del(path: string): Promise<unknown> {
    return this.request('DELETE', path);
  }

  /** GET returning the raw body, read up to maxBytes (truncated beyond). */
  async getRaw(path: string, maxBytes: number): Promise<RawResponse> {
    const res = await this.fetchRaw('GET', path);
    const contentType = res.headers.get('content-type') || 'application/octet-stream';
    const reader = res.body?.getReader();
    const chunks: Uint8Array[] = [];
    let total = 0;
    let truncated = false;
    if (reader) {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        if (total + value.length > maxBytes) {
          chunks.push(value.subarray(0, maxBytes - total));
          total = maxBytes;
          truncated = true;
          await reader.cancel();
          break;
        }
        chunks.push(value);
        total += value.length;
      }
    }
    const bytes = new Uint8Array(total);
    let off = 0;
    for (const c of chunks) {
      bytes.set(c, off);
      off += c.length;
    }
    return { contentType, bytes, truncated };
  }

  /** Validate the token by calling the profile endpoint; records its scope. */
  async validateToken(): Promise<boolean> {
    try {
      const profile = (await this.get('/platform/auth/account/profile')) as {
        token?: { scoped?: boolean; project_id?: string; role?: string };
      };
      this.scope = scopeFromProfile(profile);
      return true;
    } catch {
      return false;
    }
  }
}

/** The token scope from a profile response (legacy tokens / sessions: not scoped). */
export function scopeFromProfile(profile: { token?: { scoped?: boolean; project_id?: string; role?: string } } | null | undefined): TokenScope {
  const t = profile?.token;
  if (!t?.scoped || !t.project_id) return { scoped: false };
  const role = t.role === 'developer' || t.role === 'admin' ? t.role : 'viewer';
  return { scoped: true, projectId: t.project_id, role };
}

/**
 * Turn a refusal into something the model (and user) can act on: a token
 * outside its project, or without enough access for the operation.
 */
export function explain(message: string, status: number, code: string | undefined, scope: TokenScope, path = ''): string {
  if (code === 'pat_out_of_scope') {
    return `${message}\nThis token is for one project${scope.projectId ? ` (${scope.projectId})` : ''} and can't reach anything else. Create a token for the other project in the Eurobase console → Account → Personal Access Tokens.`;
  }
  if (status === 403 && scope.scoped) {
    const hint = scope.role === 'viewer'
      ? 'This token is read-only. For reads, use queryTable (rows, filters, counts). To make changes, create a Developer token for this project in the Eurobase console → Account → Personal Access Tokens.'
      : `This token has ${scope.role} access, which isn't enough for this${scope.role === 'developer' ? ' (secrets, end users and API keys need Admin)' : ''}. Create a token with more access in the Eurobase console → Account → Personal Access Tokens.`;
    return `${message}\n${hint}`;
  }
  // A 404 for ANOTHER project than the token's (not a missing file / table
  // in its own project).
  const pathProject = /^\/platform\/projects\/([^/?#]+)/.exec(path)?.[1];
  if (status === 404 && scope.scoped && pathProject && pathProject.toLowerCase() !== scope.projectId?.toLowerCase()) {
    return `${message}\nThis token only works for project ${scope.projectId}.`;
  }
  return message;
}

/** read_only for the SQL tools: legacy account-wide tokens stay read-only
 *  (unchanged behaviour); for a project-scoped token the gateway decides by
 *  its role. */
export function sqlReadOnly(scope: TokenScope): boolean {
  return !scope.scoped;
}
