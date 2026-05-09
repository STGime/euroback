// Tests for the vault read path. Closes #79.
//
// We focus on the decrypt half: given an AES-256-GCM ciphertext + nonce
// produced exactly the way the Go gateway produces it
// (gcm.Seal(nil, nonce, plaintext, nil) → ciphertext || tag), the runner
// must recover the plaintext. The DB query is mocked via a stub `db`
// tagged-template so the tests are hermetic.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { resolveVaultSecret } from "./vault.ts";

const KEY_RAW = new Uint8Array(32);
for (let i = 0; i < 32; i++) KEY_RAW[i] = i;
const KEY_B64 = btoa(String.fromCharCode(...KEY_RAW));

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
function stubDB(rows: Array<{ encrypted: Uint8Array; nonce: Uint8Array }>): any {
  return (_strings: TemplateStringsArray, ..._values: unknown[]) => Promise.resolve(rows);
}

// deno-lint-ignore no-explicit-any
function stubDBThrowing(message: string): any {
  return (_strings: TemplateStringsArray, ..._values: unknown[]) => Promise.reject(new Error(message));
}

Deno.test("vault.get round-trips a Go-encrypted secret", async () => {
  Deno.env.set("VAULT_ENCRYPTION_KEY", KEY_B64);
  const { encrypted, nonce } = await encryptForGateway("fal_ai_key_value", KEY_RAW);
  const got = await resolveVaultSecret(stubDB([{ encrypted, nonce }]), "11111111-2222-3333-4444-555555555555", "FAL_AI_KEY");
  assertEquals(got, "fal_ai_key_value");
});

Deno.test("vault.get returns null when VAULT_ENCRYPTION_KEY is unset", async () => {
  Deno.env.delete("VAULT_ENCRYPTION_KEY");
  const { encrypted, nonce } = await encryptForGateway("x", KEY_RAW);
  const got = await resolveVaultSecret(stubDB([{ encrypted, nonce }]), "p", "K");
  assertEquals(got, null);
});

Deno.test("vault.get returns null when the secret does not exist", async () => {
  Deno.env.set("VAULT_ENCRYPTION_KEY", KEY_B64);
  const got = await resolveVaultSecret(stubDB([]), "11111111-2222-3333-4444-555555555555", "MISSING");
  assertEquals(got, null);
});

Deno.test("vault.get returns null when the DB query throws (no exception leak)", async () => {
  Deno.env.set("VAULT_ENCRYPTION_KEY", KEY_B64);
  const got = await resolveVaultSecret(stubDBThrowing("permission denied"), "11111111-2222-3333-4444-555555555555", "X");
  assertEquals(got, null);
});

Deno.test("vault.get returns null with no projectId or name (no DB call)", async () => {
  let called = false;
  // deno-lint-ignore no-explicit-any
  const db: any = () => {
    called = true;
    return Promise.resolve([]);
  };
  Deno.env.set("VAULT_ENCRYPTION_KEY", KEY_B64);
  assertEquals(await resolveVaultSecret(db, "", "X"), null);
  assertEquals(await resolveVaultSecret(db, "p", ""), null);
  assertEquals(called, false);
});

Deno.test("vault.get returns null when ciphertext was tampered (auth tag mismatch)", async () => {
  Deno.env.set("VAULT_ENCRYPTION_KEY", KEY_B64);
  const { encrypted, nonce } = await encryptForGateway("secret", KEY_RAW);
  encrypted[0] ^= 0xff; // flip a bit in the ciphertext
  const got = await resolveVaultSecret(stubDB([{ encrypted, nonce }]), "p", "K");
  assertEquals(got, null);
});
