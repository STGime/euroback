// Vault read path for edge functions. Closes #79.
//
// Worker calls ctx.vault.get(name) → bridge.ts emits a vault.get.call →
// server.ts (parent) calls resolveVaultSecret() here → we query
// public.vault_get_for_runner(projectId, name) (SECURITY DEFINER, granted
// only to eurobase_function_runner, added by migration 000049), get back
// the encrypted blob and nonce, and decrypt locally with VAULT_ENCRYPTION_KEY.
//
// Layout compatibility with the Go gateway:
//   gcm.Seal(nil, nonce, plaintext, nil)  → ciphertext || tag (16 bytes)
//   crypto.subtle.decrypt({name:"AES-GCM", iv}, key, blob)
// Web Crypto's AES-GCM expects the auth tag appended to the ciphertext —
// same layout as Go's GCM, so we pass the bytea blob through directly.

// Lazy: read VAULT_ENCRYPTION_KEY on first use rather than at module
// load. Lets tests set the env var after import without resorting to
// cache-busting URL params, and matches how the gateway boots vault.
let _key: CryptoKey | null = null;
let _keyEnv: string | null = null;

async function getCryptoKey(): Promise<CryptoKey | null> {
  const env = Deno.env.get("VAULT_ENCRYPTION_KEY") ?? "";
  if (_key && env === _keyEnv) return _key;
  // Reset if env changed (only meaningful in tests; in production the
  // pod env is fixed for its lifetime).
  _key = null;
  _keyEnv = env;
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
  _key = await crypto.subtle.importKey("raw", raw, "AES-GCM", false, ["decrypt"]);
  return _key;
}

// resolveVaultSecret returns the decrypted secret value for (projectId, name)
// or null when the secret doesn't exist, the encryption key isn't configured,
// or decryption fails. All failure modes return null deliberately — the
// worker contract is `string | null`, and surfacing exceptions through the
// RPC bridge would just leak internals to user code.
export async function resolveVaultSecret(
  // deno-lint-ignore no-explicit-any
  db: any,
  projectId: string,
  name: string,
): Promise<string | null> {
  if (!projectId || !name) return null;
  const key = await getCryptoKey();
  if (!key) {
    console.warn("[vault] VAULT_ENCRYPTION_KEY not configured; ctx.vault.get returns null");
    return null;
  }

  // postgres.js returns bytea columns as Uint8Array. The SECURITY DEFINER
  // function returns at most one row (vault_secrets.name has a unique
  // constraint) — destructure with a length guard for safety.
  let rows: Array<{ encrypted: Uint8Array; nonce: Uint8Array }>;
  try {
    rows = await db`SELECT encrypted, nonce FROM public.vault_get_for_runner(${projectId}::uuid, ${name})`;
  } catch (err) {
    console.error("[vault] DB read failed", err instanceof Error ? err.message : err);
    return null;
  }
  if (!rows || rows.length === 0) return null;
  const { encrypted, nonce } = rows[0];
  if (!encrypted || !nonce) return null;

  try {
    const plain = await crypto.subtle.decrypt(
      { name: "AES-GCM", iv: nonce },
      key,
      encrypted,
    );
    return new TextDecoder().decode(plain);
  } catch (err) {
    console.error("[vault] decryption failed", err instanceof Error ? err.message : err);
    return null;
  }
}
