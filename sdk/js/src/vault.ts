/**
 * Vault client for encrypted secrets storage.
 *
 * Only works with a **secret** API key (eb_sk_*). Public keys are rejected
 * by the gateway with 403.
 */

import type { EurobaseConfig } from './http'

export interface VaultSecretMeta {
  name: string
  description: string
  created_at: string
  updated_at: string
}

export interface VaultSecret extends VaultSecretMeta {
  id: string
  value: string
}

export class VaultClient {
  private config: EurobaseConfig

  constructor(config: EurobaseConfig) {
    this.config = config
  }

  private get baseUrl(): string {
    return this.config.url.replace(/\/+$/, '')
  }

  private headers(): Record<string, string> {
    return {
      'apikey': this.config.apiKey,
      'Content-Type': 'application/json',
    }
  }

  /**
   * List all secret names and descriptions (values are never returned).
   */
  async list(): Promise<{ data: VaultSecretMeta[] | null; error: string | null }> {
    try {
      const res = await fetch(`${this.baseUrl}/v1/vault`, {
        method: 'GET',
        headers: this.headers(),
      })
      if (!res.ok) {
        const body = await res.json().catch(() => ({}))
        return { data: null, error: body?.error || `HTTP ${res.status}` }
      }
      const data = await res.json()
      return { data, error: null }
    } catch (err) {
      return { data: null, error: (err as Error).message }
    }
  }

  /**
   * Get a decrypted secret by name.
   */
  async get(name: string): Promise<{ data: string | null; error: string | null }> {
    try {
      const res = await fetch(`${this.baseUrl}/v1/vault/${encodeURIComponent(name)}`, {
        method: 'GET',
        headers: this.headers(),
      })
      if (!res.ok) {
        const body = await res.json().catch(() => ({}))
        return { data: null, error: body?.error || `HTTP ${res.status}` }
      }
      const secret: VaultSecret = await res.json()
      return { data: secret.value, error: null }
    } catch (err) {
      return { data: null, error: (err as Error).message }
    }
  }

  /**
   * Create or update a secret.
   */
  async set(name: string, value: string, description?: string): Promise<{ error: string | null }> {
    try {
      const res = await fetch(`${this.baseUrl}/v1/vault`, {
        method: 'POST',
        headers: this.headers(),
        body: JSON.stringify({ name, value, description: description ?? '' }),
      })
      if (!res.ok) {
        const body = await res.json().catch(() => ({}))
        return { error: body?.error || `HTTP ${res.status}` }
      }
      return { error: null }
    } catch (err) {
      return { error: (err as Error).message }
    }
  }

  /**
   * Delete a secret.
   */
  async delete(name: string): Promise<{ error: string | null }> {
    try {
      const res = await fetch(`${this.baseUrl}/v1/vault/${encodeURIComponent(name)}`, {
        method: 'DELETE',
        headers: this.headers(),
      })
      if (!res.ok) {
        const body = await res.json().catch(() => ({}))
        return { error: body?.error || `HTTP ${res.status}` }
      }
      return { error: null }
    } catch (err) {
      return { error: (err as Error).message }
    }
  }
}
