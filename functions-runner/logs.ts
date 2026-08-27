// Structured log capture for edge functions (#492).
//
// A user function calls ctx.log.info/warn/error during an invocation.
// The pre-#492 runner buffered these to the pod's stdout only — only
// visible via kubectl. This module additionally captures a structured
// shape the gateway can pull off the response header and persist to
// edge_function_logs.log_lines (migration 000109), which the console
// then renders per-invocation.
//
// Extracted from server.ts so it's unit-testable in isolation.

/** A single ctx.log.* line, in the shape the console consumes and
 *  migration 000109 stores. Kept flat to keep the JSONB payload small
 *  — the 10 KB cap includes JSON overhead. */
export interface CapturedLogLine {
  level: "INFO" | "WARN" | "ERROR";
  msg: string;
  data?: unknown;
  ts: string; // ISO-8601 at capture time
}

export interface LogCapture {
  info(msg: string, data?: Record<string, unknown>): void;
  warn(msg: string, data?: Record<string, unknown>): void;
  error(msg: string, data?: Record<string, unknown>): void;
  getLines(): CapturedLogLine[];
}

/** Build a per-invocation log capture bound to `projectId` (used in
 *  the stdout mirror line so pod logs stay grep-able by tenant).
 *
 *  `limit` is the max total *content* bytes across captured entries —
 *  10 KB in production (LOG_OUTPUT_LIMIT). Once exceeded, a single
 *  truncation-sentinel line is appended and further ctx.log.* calls
 *  in this invocation become no-ops (stdout mirror also stops, since
 *  a runaway function shouldn't be able to flood pod logs either). */
export function createLogCapture(projectId: string, limit: number): LogCapture {
  let totalBytes = 0;
  const lines: CapturedLogLine[] = [];
  let truncated = false;

  function capture(level: "INFO" | "WARN" | "ERROR", msg: string, data?: Record<string, unknown>) {
    if (truncated) return;

    const entry: CapturedLogLine = { level, msg, ts: new Date().toISOString() };
    if (data !== undefined) entry.data = data;
    const entryBytes = new TextEncoder().encode(JSON.stringify(entry)).length;

    if (totalBytes + entryBytes > limit) {
      truncated = true;
      lines.push({
        level: "WARN",
        msg: `Log output truncated at ${limit} bytes — subsequent ctx.log.* calls this invocation were dropped.`,
        ts: new Date().toISOString(),
      });
      return;
    }
    totalBytes += entryBytes;
    lines.push(entry);

    // Mirror to pod stdout so kubectl remains a debugging surface for
    // platform ops. Same prefixed shape the pre-#492 runner used so any
    // log-scraping tooling continues to work.
    const stdoutLine = `[fn:${projectId}] ${level}: ${msg}${data ? " " + JSON.stringify(data) : ""}`;
    if (level === "ERROR") console.error(stdoutLine);
    else if (level === "WARN") console.warn(stdoutLine);
    else console.log(stdoutLine);
  }

  return {
    info: (msg, data) => capture("INFO", msg, data),
    warn: (msg, data) => capture("WARN", msg, data),
    error: (msg, data) => capture("ERROR", msg, data),
    getLines: () => lines,
  };
}

/** Encode captured lines as a base64 JSON string safe to place in an
 *  HTTP response header. Returns "" when there are no lines so the
 *  caller can skip setting the header entirely (`""` header value is
 *  a bad signal — some intermediaries strip empty headers, others
 *  don't, better to not send at all). */
export function encodeLogLinesHeader(lines: CapturedLogLine[]): string {
  if (lines.length === 0) return "";
  const json = JSON.stringify(lines);
  // btoa needs a binary string; TextEncoder gives us the utf-8 bytes,
  // then we widen each byte to a code unit for btoa. Round-trips
  // through JSON.parse(atob(...)) on the decode side.
  const bytes = new TextEncoder().encode(json);
  let binary = "";
  for (const b of bytes) binary += String.fromCharCode(b);
  return btoa(binary);
}
