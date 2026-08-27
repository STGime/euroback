// Tests for structured log capture (#492). The runner emits captured
// lines in the X-Function-Logs response header; these tests pin the
// contract the Go gateway (internal/functions/invoke.go
// decodeFunctionLogsHeader) expects: base64(JSON.stringify(lines)).

import { assertEquals, assert } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { createLogCapture, encodeLogLinesHeader } from "./logs.ts";

Deno.test("captures info/warn/error with data payload preserved", () => {
  const cap = createLogCapture("proj_1", 10_000);
  cap.info("hello", { user: 42 });
  cap.warn("careful");
  cap.error("boom", { code: "E_X" });

  const lines = cap.getLines();
  assertEquals(lines.length, 3);
  assertEquals(lines[0].level, "INFO");
  assertEquals(lines[0].msg, "hello");
  assertEquals(lines[0].data, { user: 42 });
  assertEquals(lines[1].level, "WARN");
  assertEquals(lines[1].data, undefined);
  assertEquals(lines[2].level, "ERROR");
  assertEquals(lines[2].data, { code: "E_X" });
});

Deno.test("truncates once the byte limit is exceeded and appends a sentinel", () => {
  // Give the cap ~200 bytes: enough for maybe two small lines, then trip.
  const cap = createLogCapture("proj_1", 200);
  cap.info("first");
  cap.info("second");
  cap.info("third with padding to trip the limit — " + "x".repeat(300));
  cap.info("fourth — should be dropped");

  const lines = cap.getLines();
  // Sentinel is a WARN line; further calls are dropped, so the last
  // entry must be the sentinel and there must be zero entries after.
  assert(lines.length >= 1);
  const last = lines[lines.length - 1];
  assertEquals(last.level, "WARN");
  assert(last.msg.includes("truncated"), `sentinel msg: ${last.msg}`);
  // The dropped "fourth" call must NOT appear anywhere.
  assert(!lines.some((l) => l.msg.startsWith("fourth")));
});

Deno.test("encodeLogLinesHeader round-trips through base64+JSON", () => {
  const cap = createLogCapture("proj_1", 10_000);
  cap.info("unicode ✓ 世界", { n: 1 });
  const header = encodeLogLinesHeader(cap.getLines());
  assert(header.length > 0);

  // Reverse: atob → utf-8 → JSON. Must match the runner's captured shape.
  const binary = atob(header);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  const decoded = JSON.parse(new TextDecoder().decode(bytes));
  assertEquals(decoded.length, 1);
  assertEquals(decoded[0].level, "INFO");
  assertEquals(decoded[0].msg, "unicode ✓ 世界");
  assertEquals(decoded[0].data, { n: 1 });
});

Deno.test("encodeLogLinesHeader returns empty string for no lines", () => {
  // Caller should skip setting the header entirely when this returns "".
  assertEquals(encodeLogLinesHeader([]), "");
});
