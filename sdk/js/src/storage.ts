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
}

// ---------------------------------------------------------------------------
// StorageClient
// ---------------------------------------------------------------------------

/** Client for interacting with Eurobase object storage. */
export class StorageClient {
  private http: HttpClient

  constructor(config: EurobaseConfig) {
    this.http = httpClient(config)
  }

  /**
   * Upload a file.
   * Uses multipart/form-data via POST /v1/storage/upload.
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

    const res = await this.http.post('/v1/storage/signed-url', body)

    if (res.error) {
      return { url: '', expires_at: '', error: res.error }
    }

    return {
      url: res.url ?? '',
      expires_at: res.expires_at ?? '',
      error: null,
    }
  }
}
