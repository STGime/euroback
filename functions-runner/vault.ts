// Vault read path for edge functions. Closes #79; key_version support
// closes #201.
//
// Worker calls ctx.vault.get(name) → bridge.ts emits a vault.get.call →
// server.ts (parent) calls resolveVaultSecret() here → we query
// public.vault_get_for_runner(projectId, name) (SECURITY DEFINER, granted
// only to eurobase_function_runner; migration 000049, key_version added in
// 000061), get back the encrypted blob + nonce + key_version, and decrypt
// locally.
//
// Key derivation mirrors internal/vault/keyprovider.go exactly:
//   key_version 0  → the raw 32-byte master (VAULT_ENCRYPTION_KEY decoded);
//                    rows that predate per-tenant keys (migration 000057).
//   key_version ≥1 → HKDF-SHA256(master, salt = tenant schema name,
//                    info = "eurobase-vault-v<version>") — the per-tenant
//                    derived key the gateway seals new writes with.
// The schema name passed in MUST be the same value the gateway used as the
// HKDF salt (projects.schema_name); server.ts takes it from the
// HMAC-verified X-Schema-Name header.
//
// Layout compatibility with the Go gateway:
//   gcm.Seal(nil, nonce, plaintext, nil)  → ciphertext || tag (16 bytes)
//   crypto.subtle.decrypt({name:"AES-GCM", iv}, key, blob)
// Web Crypto's AES-GCM expects the auth tag appended to the ciphertext —
// same layout as Go's GCM, so we pass the bytea blob through directly.

// VaultResult distinguishes "the secret does not exist" (value: null) from
// "the platform could not read it" (error) — closes the silent-null half
// of #201. The error string surfaces to user code as a thrown Error via
// the RPC bridge, so a developer can tell a missing secret apart from a
// misconfigured runtime without pod access.
export type VaultResult = { value: string | null } | { error: string };

// Lazy: read VAULT_ENCRYPTION_KEY on first use rather than at module
// load. Lets tests set the env var after import without resorting to
// cache-busting URL params, and matches how the gateway boots vault.
let _masterEnv: string | null = null;
let _masterRaw: Uint8Array | null = null;
// Imported CryptoKeys per (version, schema) — derivation is cheap but
// not free, and the same few tenants invoke repeatedly.
const _keyCache = new Map<string, CryptoKey>();

function getMasterRaw(): Uint8Array | null {
  const env = Deno.env.get("VAULT_ENCRYPTION_KEY") ?? "";
  if (env === _masterEnv) return _masterRaw;
  // Reset if env changed (only meaningful in tests; in production the
  // pod env is fixed for its lifetime).
  _masterEnv = env;
  _masterRaw = null;
  _keyCache.clear();
  if (!env) return null;
  let raw: Uint8Array;
  try {
    raw = Uint8Array.from(atob(env), (c) => c.charCodeAt(0));
  } catch (_) {
    console.error("[vault] VAULT_ENCRYPTION_KEY is not valid base64");
    return null;
  }
  if (raw.byteLength !== 32) {
    console.error(`[vault] VAULT_ENCRYPTION_KEY must decode to 32 bytes (got ${raw.byteLength})`);
    return null;
  }
  _masterRaw = raw;
  return raw;
}

// getAesKey returns the AES-GCM decrypt key for (schemaName, version),
// mirroring keyprovider.go DeriveKey. Throws when version ≥1 but no
// schema name is available to derive with.
async function getAesKey(
  master: Uint8Array,
  schemaName: string,
  version: number,
): Promise<CryptoKey> {
  const cacheKey = version === 0 ? "v0" : `v${version}:${schemaName}`;
  const hit = _keyCache.get(cacheKey);
  if (hit) return hit;

  let rawKey: Uint8Array = master;
  if (version !== 0) {
    if (!schemaName) {
      throw new Error("missing tenant schema name for per-tenant key derivation");
    }
    const te = new TextEncoder();
    const hkdfKey = await crypto.subtle.importKey("raw", master, "HKDF", false, ["deriveBits"]);
    const bits = await crypto.subtle.deriveBits(
      {
        name: "HKDF",
        hash: "SHA-256",
        salt: te.encode(schemaName),
        info: te.encode("eurobase-vault-v" + version),
      },
      hkdfKey,
      256,
    );
    rawKey = new Uint8Array(bits);
  }
  const key = await crypto.subtle.importKey("raw", rawKey, "AES-GCM", false, ["decrypt"]);
  _keyCache.set(cacheKey, key);
  return key;
}

// openSealed decrypts a sealed (blob, nonce, version) trio for the given
// tenant schema. Same KDF + AEAD pipeline as resolveVaultSecret — split
// out so the env_vars decrypt path on edge_functions (#206) can reuse it
// without duplicating the HKDF/AES-GCM ceremony.
//
// Throws on misconfiguration (missing key, derivation failure, wrong key)
// — caller decides whether that should fail the load or fall back to an
// empty map. The runner's loadFunction promotes any throw here into a
// loud server log and returns the function with empty env, so a
// misconfigured pod doesn't silently expose plaintext via the legacy
// column.
export async function openSealed(
  schemaName: string,
  blob: Uint8Array,
  nonce: Uint8Array,
  keyVersion: number,
): Promise<string> {
  const master = getMasterRaw();
  if (!master) {
    throw new Error("VAULT_ENCRYPTION_KEY missing — cannot decrypt sealed value");
  }
  const key = await getAesKey(master, schemaName, keyVersion);
  const plain = await crypto.subtle.decrypt(
    { name: "AES-GCM", iv: nonce },
    key,
    blob,
  );
  return new TextDecoder().decode(plain);
}

// resolveVaultSecret returns { value } for found/not-found secrets and
// { error } for platform-side failures (no key configured, lookup failed,
// wrong key / decryption failed). Not-found stays a plain null value —
// that one is the developer's to handle.
export async function resolveVaultSecret(
  // deno-lint-ignore no-explicit-any
  db: any,
  projectId: string,
  schemaName: string,
  name: string,
): Promise<VaultResult> {
  if (!projectId || !name) return { value: null };

  const master = getMasterRaw();
  if (!master) {
    console.warn("[vault] VAULT_ENCRYPTION_KEY not configured/invalid; ctx.vault.get unavailable");
    return {
      error: "vault unavailable: encryption key is not configured in the functions runtime — contact the platform operator",
    };
  }

  // postgres.js returns bytea columns as Uint8Array and smallint as
  // number. The SECURITY DEFINER function returns at most one row
  // (vault_secrets.name has a unique constraint).
  let rows: Array<{ encrypted: Uint8Array; nonce: Uint8Array; key_version: number | null }>;
  try {
    rows = await db`SELECT encrypted, nonce, key_version FROM public.vault_get_for_runner(${projectId}::uuid, ${name})`;
  } catch (err) {
    console.error("[vault] DB read failed", err instanceof Error ? err.message : err);
    return { error: "vault unavailable: secret lookup failed" };
  }
  if (!rows || rows.length === 0) return { value: null };
  const { encrypted, nonce } = rows[0];
  if (!encrypted || !nonce) return { value: null };
  const version = Number(rows[0].key_version ?? 0);

  let key: CryptoKey;
  try {
    key = await getAesKey(master, schemaName, version);
  } catch (err) {
    console.error("[vault] key derivation failed", err instanceof Error ? err.message : err);
    return { error: `vault unavailable: key derivation failed (key_version ${version})` };
  }

  try {
    const plain = await crypto.subtle.decrypt(
      { name: "AES-GCM", iv: nonce },
      key,
      encrypted,
    );
    return { value: new TextDecoder().decode(plain) };
  } catch (err) {
    console.error(
      `[vault] decryption failed for ${name} (key_version ${version})`,
      err instanceof Error ? err.message : err,
    );
    return {
      error: `vault decryption failed for "${name}" (key_version ${version}) — the runtime's key does not match the key this secret was sealed with`,
    };
  }
}
