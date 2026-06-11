// Tests for the vault read path. Closes #79; key_version support #201.
//
// We focus on the decrypt half: given an AES-256-GCM ciphertext + nonce
// produced exactly the way the Go gateway produces it
// (gcm.Seal(nil, nonce, plaintext, nil) → ciphertext || tag), the runner
// must recover the plaintext — including secrets sealed with the
// per-tenant HKDF-derived keys the gateway uses since migration 000057.
// The DB query is mocked via a stub `db` tagged-template so the tests
// are hermetic.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { resolveVaultSecret } from "./vault.ts";

const KEY_RAW = new Uint8Array(32);
for (let i = 0; i < 32; i++) KEY_RAW[i] = i;
const KEY_B64 = btoa(String.fromCharCode(...KEY_RAW));

const PROJECT_ID = "11111111-2222-3333-4444-555555555555";
const SCHEMA = "tenant_11111111_2222_3333_4444_555555555555";

// Encrypt with the same layout the Go gateway uses so the test exercises
// the actual on-disk byte format. iv is 12 bytes (GCM standard / what
// crypto/cipher's NewGCM uses by default).
async function encryptForGateway(
  plaintext: string,
  rawKey: Uint8Array,
): Promise<{ encrypted: Uint8Array; nonce: Uint8Array }> {
  const key = await crypto.subtle.importKey("raw", rawKey, "AES-GCM", false, ["encrypt"]);
  const nonce = crypto.getRandomValues(new Uint8Array(12));
  const buf = await crypto.subtle.encrypt(
    { name: "AES-GCM", iv: nonce },
    key,
    new TextEncoder().encode(plaintext),
  );
  return { encrypted: new Uint8Array(buf), nonce };
}

// Tagged-template stub matching postgres.js's call shape.
// deno-lint-ignore no-explicit-any
function stubDB(rows: Array<{ encrypted: Uint8Array; nonce: Uint8Array; key_version?: number }>): any {
  return (_strings: TemplateStringsArray, ..._values: unknown[]) => Promise.resolve(rows);
}

// deno-lint-ignore no-explicit-any
function stubDBThrowing(message: string): any {
  return (_strings: TemplateStringsArray, ..._values: unknown[]) => Promise.reject(new Error(message));
}

function b64ToBytes(b64: string): Uint8Array {
  return Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
}

Deno.test("vault.get round-trips a Go-encrypted legacy (key_version 0) secret", async () => {
  Deno.env.set("VAULT_ENCRYPTION_KEY", KEY_B64);
  const { encrypted, nonce } = await encryptForGateway("fal_ai_key_value", KEY_RAW);
  const got = await resolveVaultSecret(stubDB([{ encrypted, nonce, key_version: 0 }]), PROJECT_ID, SCHEMA, "FAL_AI_KEY");
  assertEquals(got, { value: "fal_ai_key_value" });
});

Deno.test("vault.get decrypts a key_version 1 secret sealed by the Go gateway (issue #201)", async () => {
  // Fixture generated with Go's crypto/hkdf + crypto/cipher, exactly as
  // internal/vault/keyprovider.go derives and service.go seals:
  //   master  = bytes 0x00..0x1f (KEY_RAW above)
  //   tenant  = SCHEMA (HKDF salt)
  //   info    = "eurobase-vault-v1"
  //   derived = RF884Rlm9sIYlm8ig2EQzav21+5drtmIg+vEujfkG54=
  //   nonce   = 0x01..0x0c
  //   sealed  = gcm.Seal(nil, nonce, "mistral_api_key_value", nil)
  // The same vector is pinned Go-side in
  // internal/vault/keyprovider_test.go so a drift in either
  // implementation fails one of the two CI jobs.
  Deno.env.set("VAULT_ENCRYPTION_KEY", KEY_B64);
  const encrypted = b64ToBytes("OYT3J2/GBpxo9TmQF9/BYzfodt28C63W91q7+g19CVRVFcSXTA==");
  const nonce = b64ToBytes("AQIDBAUGBwgJCgsM");
  const got = await resolveVaultSecret(stubDB([{ encrypted, nonce, key_version: 1 }]), PROJECT_ID, SCHEMA, "MISTRAL_API_KEY");
  assertEquals(got, { value: "mistral_api_key_value" });
});

Deno.test("vault.get errors (not null) for a key_version 1 secret decrypted with the wrong tenant (issue #201)", async () => {
  // Same v1 fixture but a different schema name → different derived key
  // → GCM auth failure. Must surface as an error so the developer can
  // tell it apart from a missing secret.
  Deno.env.set("VAULT_ENCRYPTION_KEY", KEY_B64);
  const encrypted = b64ToBytes("OYT3J2/GBpxo9TmQF9/BYzfodt28C63W91q7+g19CVRVFcSXTA==");
  const nonce = b64ToBytes("AQIDBAUGBwgJCgsM");
  const got = await resolveVaultSecret(stubDB([{ encrypted, nonce, key_version: 1 }]), PROJECT_ID, "tenant_other", "MISTRAL_API_KEY");
  if (!("error" in got)) throw new Error(`expected error result, got ${JSON.stringify(got)}`);
});

Deno.test("vault.get missing key_version is treated as legacy version 0", async () => {
  // Rollout edge: a DB row read through a stub that omits key_version
  // (pre-000061 shape) must keep decrypting with the master key.
  Deno.env.set("VAULT_ENCRYPTION_KEY", KEY_B64);
  const { encrypted, nonce } = await encryptForGateway("legacy_value", KEY_RAW);
  const got = await resolveVaultSecret(stubDB([{ encrypted, nonce }]), PROJECT_ID, SCHEMA, "LEGACY");
  assertEquals(got, { value: "legacy_value" });
});

Deno.test("vault.get errors when VAULT_ENCRYPTION_KEY is unset (issue #201: not a silent null)", async () => {
  Deno.env.delete("VAULT_ENCRYPTION_KEY");
  const { encrypted, nonce } = await encryptForGateway("x", KEY_RAW);
  const got = await resolveVaultSecret(stubDB([{ encrypted, nonce, key_version: 0 }]), "p", "s", "K");
  if (!("error" in got)) throw new Error(`expected error result, got ${JSON.stringify(got)}`);
});

Deno.test("vault.get returns null value when the secret does not exist", async () => {
  Deno.env.set("VAULT_ENCRYPTION_KEY", KEY_B64);
  const got = await resolveVaultSecret(stubDB([]), PROJECT_ID, SCHEMA, "MISSING");
  assertEquals(got, { value: null });
});

Deno.test("vault.get errors when the DB query throws (no exception leak)", async () => {
  Deno.env.set("VAULT_ENCRYPTION_KEY", KEY_B64);
  const got = await resolveVaultSecret(stubDBThrowing("permission denied"), PROJECT_ID, SCHEMA, "X");
  if (!("error" in got)) throw new Error(`expected error result, got ${JSON.stringify(got)}`);
});

Deno.test("vault.get returns null with no projectId or name (no DB call)", async () => {
  let called = false;
  // deno-lint-ignore no-explicit-any
  const db: any = () => {
    called = true;
    return Promise.resolve([]);
  };
  Deno.env.set("VAULT_ENCRYPTION_KEY", KEY_B64);
  assertEquals(await resolveVaultSecret(db, "", SCHEMA, "X"), { value: null });
  assertEquals(await resolveVaultSecret(db, "p", SCHEMA, ""), { value: null });
  assertEquals(called, false);
});

Deno.test("vault.get errors when ciphertext was tampered (auth tag mismatch)", async () => {
  Deno.env.set("VAULT_ENCRYPTION_KEY", KEY_B64);
  const { encrypted, nonce } = await encryptForGateway("secret", KEY_RAW);
  encrypted[0] ^= 0xff; // flip a bit in the ciphertext
  const got = await resolveVaultSecret(stubDB([{ encrypted, nonce, key_version: 0 }]), PROJECT_ID, SCHEMA, "K");
  if (!("error" in got)) throw new Error(`expected error result, got ${JSON.stringify(got)}`);
});
