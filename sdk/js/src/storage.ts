/**
 * Storage client for Eurobase's S3-backed object storage.
 * All operations go through the gateway at /v1/storage/*.
 */

import type { EurobaseConfig } from './http'
import { httpClient, type HttpClient } from './http'

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

/** Returned after a successful file upload. */
export interface UploadResult {
  key: string
  content_type: string
  size: number
  error: string | null
}

/** Returned after generating a pre-signed URL. */
export interface SignedUrlResult {
  url: string
  expires_at: string
  /**
   * Headers the upload must send exactly as given (e.g. a retention policy
   * on Legal Team projects). Empty for most uploads and all downloads.
   */
  headers: Record<string, string>
  error: string | null
}

/** Info about a single stored object. */
export interface ObjectInfo {
  key: string
  size: number
  last_modified: string
  content_type?: string
}

/** Returned from the list operation. */
export interface ListResult {
  objects: ObjectInfo[]
  next_cursor: string | null
  has_more: boolean
  error: string | null
}

/** Options for the list operation. */
export interface ListOptions {
  prefix?: string
  limit?: number
  cursor?: string
}

/** Options for file upload. */
export interface UploadOptions {
  contentType?: string
}

/**
 * Check whether a value is a Node.js Buffer without requiring @types/node.
 * This keeps the SDK zero-dependency while still supporting Buffer uploads.
 */
function isBuffer(value: unknown): value is { buffer: ArrayBuffer; byteOffset: number; byteLength: number } {
  return (
    typeof value === 'object' &&
    value !== null &&
    typeof (value as any).constructor?.name === 'string' &&
    (value as any).constructor.name === 'Buffer' &&
    typeof (value as any).byteLength === 'number'
  )
}

/** Options for signed URL generation. */
export interface SignedUrlOptions {
  expiresIn?: number
  contentType?: string
  /**
   * Upload only: the file's exact size in bytes. It is checked against your
   * plan's upload limit, and the URL then accepts exactly that many bytes.
   */
  size?: number
}

/**
 * Files larger than this go straight to storage through a signed upload
 * URL instead of through the API (which takes request bodies up to 50 MB),
 * so they can reach the plan's upload limit (500 MB on Team).
 */
export const DIRECT_UPLOAD_THRESHOLD = 45 * 1024 * 1024

// ---------------------------------------------------------------------------
// StorageClient
// ---------------------------------------------------------------------------

/** Client for interacting with Eurobase object storage. */
export class StorageClient {
  private http: HttpClient
  private config: EurobaseConfig

  constructor(config: EurobaseConfig, http?: HttpClient) {
    this.config = config
    this.http = http ?? httpClient(config)
  }

  /**
   * A plain URL for a file in a **public** shared folder — usable directly
   * in `<img src>`, `<audio src>`, CSS, etc. No sign-in or request needed.
   *
   * The URL carries your project's **public** key (`?apikey=eb_pk_…`); it
   * works only for files the developer uploaded into a folder shared as
   * "Public" (console → Storage → Sharing). Anything else returns 404. For
   * folders shared with signed-in users only, use `createSignedUrl(key,
   * 'download')` instead.
   *
   * Throws if the client was created with a secret key — a secret key must
   * never end up in a URL.
   */
  getPublicUrl(key: string): string {
    if (this.config.apiKey.startsWith('eb_sk_')) {
      throw new Error('getPublicUrl() needs the public key (eb_pk_…); never put the secret key in a URL')
    }
    const base = this.config.url.replace(/\/+$/, '')
    const path = key.split('/').map(encodeURIComponent).join('/')
    return `${base}/v1/storage/${path}?apikey=${encodeURIComponent(this.config.apiKey)}`
  }

  /**
   * Upload a file.
   * Uses multipart/form-data via POST /v1/storage/upload; files over
   * 45 MB go straight to storage through a signed upload URL, up to the
   * plan's upload limit.
   */
  async upload(
    key: string,
    file: File | Blob | ArrayBuffer | Uint8Array,
    options?: UploadOptions,
  ): Promise<UploadResult> {
    const formData = new FormData()

    // Handle ArrayBuffer, Uint8Array, and Node.js Buffer by wrapping in a Blob.
    let blob: Blob
    if (isBuffer(file)) {
      blob = new Blob([new Uint8Array(file.buffer, file.byteOffset, file.byteLength)], {
        type: options?.contentType || 'application/octet-stream',
      })
    } else if (file instanceof ArrayBuffer) {
      blob = new Blob([file], { type: options?.contentType || 'application/octet-stream' })
    } else if (file instanceof Uint8Array) {
      blob = new Blob([file as BlobPart], { type: options?.contentType || 'application/octet-stream' })
    } else {
      blob = file as Blob
    }

    if (blob.size > DIRECT_UPLOAD_THRESHOLD) {
      return this.uploadDirect(key, blob, options?.contentType || blob.type || 'application/octet-stream')
    }

    formData.append('file', blob, key)
    formData.append('key', key)

    const res = await this.http.postForm('/v1/storage/upload', formData)

    if (res.error) {
      return { key: '', content_type: '', size: 0, error: res.error }
    }

    return {
      key: res.key ?? key,
      content_type: res.content_type ?? '',
      size: res.size ?? 0,
      error: null,
    }
  }

  /**
   * Upload a large file straight to storage: a signed upload URL for its
   * exact size (checked against the plan's upload limit), then a PUT.
   */
  private async uploadDirect(key: string, blob: Blob, contentType: string): Promise<UploadResult> {
    const signed = await this.createSignedUrl(key, 'upload', { contentType, size: blob.size })
    if (signed.error) {
      return { key: '', content_type: '', size: 0, error: signed.error }
    }
    let res: Response
    try {
      res = await fetch(signed.url, {
        method: 'PUT',
        headers: { 'Content-Type': contentType, ...signed.headers },
        body: blob,
      })
    } catch (err) {
      return { key: '', content_type: '', size: 0, error: `upload failed: ${(err as Error).message}` }
    }
    if (!res.ok) {
      return { key: '', content_type: '', size: 0, error: `upload failed: storage answered ${res.status}` }
    }
    return { key, content_type: contentType, size: blob.size, error: null }
  }

  /**
   * Download a file by key.
   * GET /v1/storage/{key}
   */
  async download(key: string): Promise<Blob> {
    return this.http.getBlob(`/v1/storage/${encodeURIComponent(key)}`)
  }

  /**
   * Delete a file by key.
   * DELETE /v1/storage/{key}
   */
  async remove(key: string): Promise<{ error: string | null }> {
    const res = await this.http.del(`/v1/storage/${encodeURIComponent(key)}`)
    return { error: res.error ?? null }
  }

  /**
   * List files in storage.
   * GET /v1/storage?prefix=...&limit=...&cursor=...
   */
  async list(options?: ListOptions): Promise<ListResult> {
    const params: Record<string, string> = {}

    if (options?.prefix) {
      params['prefix'] = options.prefix
    }
    if (options?.limit !== undefined) {
      params['limit'] = String(options.limit)
    }
    if (options?.cursor) {
      params['cursor'] = options.cursor
    }

    const res = await this.http.get('/v1/storage', params)

    if (res.error) {
      return { objects: [], next_cursor: null, has_more: false, error: res.error }
    }

    return {
      objects: res.objects ?? [],
      next_cursor: res.next_cursor ?? null,
      has_more: res.has_more ?? false,
      error: null,
    }
  }

  /**
   * Generate a pre-signed URL for upload or download.
   * POST /v1/storage/signed-url
   */
  async createSignedUrl(
    key: string,
    operation: 'upload' | 'download',
    options?: SignedUrlOptions,
  ): Promise<SignedUrlResult> {
    const body: Record<string, any> = { key, operation }

    if (options?.expiresIn !== undefined) {
      body['expires_in'] = options.expiresIn
    }
    if (options?.contentType) {
      body['content_type'] = options.contentType
    }
    if (options?.size !== undefined) {
      body['size'] = options.size
    }

    const res = await this.http.post('/v1/storage/signed-url', body)

    if (res.error) {
      return { url: '', expires_at: '', headers: {}, error: res.error }
    }

    return {
      url: res.url ?? '',
      expires_at: res.expires_at ?? '',
      headers: res.headers ?? {},
      error: null,
    }
  }
}
