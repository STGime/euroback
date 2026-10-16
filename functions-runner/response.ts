/**
 * Turns a function's serialized response into the runner's HTTP response.
 *
 * The Response constructor throws for a body on a null-body status (204,
 * 205, 304: "Response with null body status cannot have body", even for
 * an empty body) and for a status outside 200–599 (RangeError). It runs in
 * the worker's message listener, so a throw there left the request without
 * an answer. A function returning 204 is normal; an invalid status is the
 * function's mistake and becomes a 500 for that one request.
 */
const NULL_BODY_STATUSES = new Set([204, 205, 304]);

export type BuiltResponse =
  | { ok: true; status: number; body: Uint8Array | null }
  | { ok: false; error: string };

export function buildResponseBody(status: unknown, body: Uint8Array | null | undefined): BuiltResponse {
  if (typeof status !== "number" || !Number.isInteger(status) || status < 200 || status > 599) {
    return { ok: false, error: `function returned an invalid HTTP status: ${String(status)} (must be 200–599)` };
  }
  if (NULL_BODY_STATUSES.has(status)) return { ok: true, status, body: null };
  return { ok: true, status, body: body ?? new Uint8Array() };
}
