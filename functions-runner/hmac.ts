// Closes layer 3 of advisory GHSA-7428-mvpp-rhr7 (C3): authenticated
// gateway → runner traffic.
//
// The runner verifies an HMAC-SHA256 signature attached to /invoke
// requests by the gateway. Without the shared secret, a cluster-internal
// attacker cannot forge a valid request — even if they reach
// `runner:8000/invoke` directly via cluster networking.
//
// This module mirrors `internal/functions/hmac.go` byte-for-byte. Any
// change to the canonical-message format MUST be applied to both sides.

const TIMESTAMP_HEADER = "X-Eurobase-Timestamp";
const SIGNATURE_HEADER = "X-Eurobase-Signature";
const MIN_SECRET_LEN = 32;

export type VerifyResult =
  | { ok: true }
  // Caller can distinguish missing headers (warn-only in soft mode) from
  // bad signature (always reject) or out-of-window timestamp (reject —
  // possible replay).
  | { ok: false; reason: "missing" | "bad_timestamp" | "out_of_window" | "mismatch" | "secret_too_short" };

export interface VerifyOptions {
  // Maximum allowed clock-skew between the request timestamp and the
  // verifier's local time, in seconds. Default 300 (5 minutes).
  maxSkewSeconds?: number;
  // Override clock for tests.
  now?: () => number;
}

export interface Verifier {
  verify(headers: Headers, opts?: VerifyOptions): Promise<VerifyResult>;
}

// newVerifier returns a Verifier that uses Web Crypto's HMAC-SHA256.
// The secret is keyed once at construction; subsequent verifies reuse
// the imported CryptoKey for performance.
export async function newVerifier(secret: string): Promise<Verifier> {
  if (secret.length < MIN_SECRET_LEN) {
    throw new Error(
      `functions runner HMAC secret must be at least ${MIN_SECRET_LEN} bytes`,
    );
  }
  const enc = new TextEncoder();
  const key = await crypto.subtle.importKey(
    "raw",
    enc.encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );

  return {
    async verify(headers: Headers, opts: VerifyOptions = {}): Promise<VerifyResult> {
      const ts = headers.get(TIMESTAMP_HEADER);
      const sig = headers.get(SIGNATURE_HEADER);
      if (!ts || !sig) return { ok: false, reason: "missing" };

      const tsNum = Number.parseInt(ts, 10);
      if (!Number.isFinite(tsNum)) return { ok: false, reason: "bad_timestamp" };

      const maxSkew = opts.maxSkewSeconds ?? 300;
      const now = opts.now ? opts.now() : Math.floor(Date.now() / 1000);
      const delta = Math.abs(now - tsNum);
      if (delta > maxSkew) return { ok: false, reason: "out_of_window" };

      const msg = canonicalMessage(headers, ts);
      const macBytes = new Uint8Array(
        await crypto.subtle.sign("HMAC", key, enc.encode(msg)),
      );
      const expectedHex = bytesToHex(macBytes);

      if (!constantTimeEqualHex(sig, expectedHex)) {
        return { ok: false, reason: "mismatch" };
      }
      return { ok: true };
    },
  };
}

// canonicalMessage MUST match internal/functions/hmac.go byte-for-byte.
//
// Format:
//
//   v=1
//   ts=<unix-seconds>
//   project=<X-Project-ID>
//   schema=<X-Schema-Name>
//   function=<X-Function-ID>
//   user=<X-User-ID, or empty>
//   email=<X-User-Email, or empty>
//   plan=<X-Plan>
//   requestid=<X-Request-ID>
//
// LF newlines. Empty values are emitted as empty strings, NOT omitted —
// stops a forged header that adds an unset field from mutating the
// canonical form to match a different signature.
export function canonicalMessage(headers: Headers, ts: string): string {
  const get = (k: string) => headers.get(k) ?? "";
  return (
    "v=1\n" +
    "ts=" + ts + "\n" +
    "project=" + get("X-Project-ID") + "\n" +
    "schema=" + get("X-Schema-Name") + "\n" +
    "function=" + get("X-Function-ID") + "\n" +
    "user=" + get("X-User-ID") + "\n" +
    "email=" + get("X-User-Email") + "\n" +
    "plan=" + get("X-Plan") + "\n" +
    "requestid=" + get("X-Request-ID")
  );
}

function bytesToHex(b: Uint8Array): string {
  let s = "";
  for (let i = 0; i < b.length; i++) {
    s += b[i].toString(16).padStart(2, "0");
  }
  return s;
}

// constantTimeEqualHex compares two hex strings in fixed time. Length
// is checked first (a length mismatch is not a secret); the rest of the
// comparison runs over min(len) bytes XORed into an accumulator that's
// only branched-on at the end. Avoids string short-circuit optimisations.
export function constantTimeEqualHex(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) {
    diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  }
  return diff === 0;
}
