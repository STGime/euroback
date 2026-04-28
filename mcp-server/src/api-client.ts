/**
 * HTTP client wrapping the Eurobase platform API.
 * Forwards the user's platform JWT for authentication.
 */

const PLATFORM_URL = process.env.EUROBASE_PLATFORM_URL || 'https://api.eurobase.app';

export class ApiClient {
  private token: string;

  constructor(token: string) {
    this.token = token;
  }

  private async request(method: string, path: string, body?: unknown): Promise<unknown> {
    const url = `${PLATFORM_URL}${path}`;
    const headers: Record<string, string> = {
      'Authorization': `Bearer ${this.token}`,
      'Content-Type': 'application/json',
    };

    const res = await fetch(url, {
      method,
      headers,
      body: body ? JSON.stringify(body) : undefined,
    });

    if (!res.ok) {
      const text = await res.text();
      let message: string;
      try {
        const json = JSON.parse(text);
        message = json.error || json.message || text;
      } catch {
        message = text;
      }
      throw new Error(`API ${method} ${path} failed (${res.status}): ${message}`);
    }

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

  /** Validate the token by calling the profile endpoint. */
  async validateToken(): Promise<boolean> {
    try {
      await this.get('/platform/auth/account/profile');
      return true;
    } catch {
      return false;
    }
  }
}
