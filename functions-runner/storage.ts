// Runner → gateway storage RPC client. Closes #85.
//
// Worker calls ctx.storage.{upload,createSignedUrl,delete} → bridge.ts
// emits a storage.*.call → server.ts (parent) calls one of the
// functions below → we sign with HMAC and POST/DELETE the gateway's
// internal endpoint at /internal/functions/storage/*.
//
// The HMAC scheme mirrors internal/functions/storage_hmac.go on the Go
// side. Same shared secret as gateway → runner (FUNCTIONS_RUNNER_HMAC_SECRET);
// distinct header names (X-Eurobase-Storage-{Timestamp,Signature})
// so signatures can't be replayed across directions.

const HMAC_SECRET = Deno.env.get("FUNCTIONS_RUNNER_HMAC_SECRET") ?? "";
// Cluster-internal name of the gateway service. Set via env when the
// runner Pod is reconfigured; the in-cluster default works today.
const GATEWAY_INTERNAL_URL = Deno.env.get("GATEWAY_INTERNAL_URL") ?? "http://gateway:8080";

type StorageOp = "storage_upload" | "storage_signed_url" | "storage_delete";

let _hmacKey: CryptoKey | null = null;

async function getHmacKey(): Promise<CryptoKey | null> {
  if (_hmacKey) return _hmacKey;
  if (!HMAC_SECRET || HMAC_SECRET.length < 32) return null;
  _hmacKey = await crypto.subtle.importKey(
    "raw",
    new TextEncoder().encode(HMAC_SECRET),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  return _hmacKey;
}

function toHex(bytes: Uint8Array): string {
  let s = "";
  for (let i = 0; i < bytes.length; i++) {
    const h = bytes[i].toString(16);
    s += h.length === 1 ? "0" + h : h;
  }
  return s;
}

async function sha256Hex(body: Uint8Array): Promise<string> {
  const buf = await crypto.subtle.digest("SHA-256", body);
  return toHex(new Uint8Array(buf));
}

function canonicalMessage(
  op: StorageOp,
  ts: string,
  projectID: string,
  schema: string,
  user: string,
  storageKey: string,
  bodyHashHex: string,
): string {
  return (
    "v=1\n" +
    "op=" + op + "\n" +
    "ts=" + ts + "\n" +
    "project=" + projectID + "\n" +
    "schema=" + schema + "\n" +
    "user=" + user + "\n" +
    "storage_key=" + storageKey + "\n" +
    "content_sha256=" + bodyHashHex
  );
}

interface CallContext {
  projectID: string;
  schemaName: string;
  userID: string;
}

// Sign + send a runner→gateway internal storage request. Adds the
// X-Eurobase-Storage-{Timestamp,Signature} headers and the identity
// headers the gateway verifier reads. Returns the parsed JSON response
// or throws on non-2xx.
async function signedFetch(opts: {
  op: StorageOp;
  method: "POST" | "DELETE";
  path: string;
  ctx: CallContext;
  storageKey: string;
  body: Uint8Array;
  contentType?: string;
}): Promise<Response> {
  const key = await getHmacKey();
  if (!key) {
    throw new Error("FUNCTIONS_RUNNER_HMAC_SECRET not configured (≥32 bytes required)");
  }
  const ts = Math.floor(Date.now() / 1000).toString();
  const bodyHashHex = await sha256Hex(opts.body);
  const msg = canonicalMessage(opts.op, ts, opts.ctx.projectID, opts.ctx.schemaName, opts.ctx.userID, opts.storageKey, bodyHashHex);
  const sigBuf = await crypto.subtle.sign("HMAC", key, new TextEncoder().encode(msg));
  const signature = toHex(new Uint8Array(sigBuf));

  const headers: Record<string, string> = {
    "X-Eurobase-Storage-Timestamp": ts,
    "X-Eurobase-Storage-Signature": signature,
    "X-Project-ID": opts.ctx.projectID,
    "X-Schema-Name": opts.ctx.schemaName,
    "X-User-ID": opts.ctx.userID,
    "X-Storage-Key": opts.storageKey,
  };
  if (opts.contentType) headers["Content-Type"] = opts.contentType;

  return await fetch(GATEWAY_INTERNAL_URL + opts.path, {
    method: opts.method,
    headers,
    body: opts.body.byteLength > 0 ? opts.body : undefined,
  });
}

export interface UploadResult {
  key?: string;
  size?: number;
  error?: string;
}

export async function uploadObject(
  ctx: CallContext,
  storageKey: string,
  body: Uint8Array,
  contentType: string,
): Promise<UploadResult> {
  try {
    const res = await signedFetch({
      op: "storage_upload",
      method: "POST",
      path: "/internal/functions/storage/upload",
      ctx,
      storageKey,
      body,
      contentType,
    });
    if (!res.ok) {
      const text = await res.text();
      return { error: `gateway ${res.status}: ${text.slice(0, 300)}` };
    }
    const data = await res.json();
    return { key: data.key, size: data.size };
  } catch (err) {
    return { error: err instanceof Error ? err.message : String(err) };
  }
}

export interface SignedURLResult {
  url?: string;
  expiresAt?: string;
  error?: string;
}

export async function createSignedUrl(
  ctx: CallContext,
  storageKey: string,
  operation: "upload" | "download",
  opts: { expiresIn?: number; contentType?: string },
): Promise<SignedURLResult> {
  try {
    const body = new TextEncoder().encode(JSON.stringify({
      key: storageKey,
      operation,
      expires_in: opts.expiresIn,
      content_type: opts.contentType,
    }));
    const res = await signedFetch({
      op: "storage_signed_url",
      method: "POST",
      path: "/internal/functions/storage/signed-url",
      ctx,
      storageKey,
      body,
      contentType: "application/json",
    });
    if (!res.ok) {
      const text = await res.text();
      return { error: `gateway ${res.status}: ${text.slice(0, 300)}` };
    }
    const data = await res.json();
    return { url: data.url, expiresAt: data.expires_at };
  } catch (err) {
    return { error: err instanceof Error ? err.message : String(err) };
  }
}

export interface DeleteResult {
  error?: string;
}

export async function deleteObject(
  ctx: CallContext,
  storageKey: string,
): Promise<DeleteResult> {
  try {
    const res = await signedFetch({
      op: "storage_delete",
      method: "DELETE",
      path: "/internal/functions/storage/" + encodeURI(storageKey),
      ctx,
      storageKey,
      body: new Uint8Array(0),
    });
    if (!res.ok && res.status !== 204) {
      const text = await res.text();
      return { error: `gateway ${res.status}: ${text.slice(0, 300)}` };
    }
    return {};
  } catch (err) {
    return { error: err instanceof Error ? err.message : String(err) };
  }
}
