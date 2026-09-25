import { assert } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { onWorkerError } from "./worker_errors.ts";

// Regression: an unhandled rejection in a worker must not take the parent
// (the runner) down. If onWorkerError stopped calling preventDefault(),
// this test process itself would exit with "Unhandled error in child
// worker".
Deno.test("an unhandled rejection in a worker does not crash the parent", async () => {
  const src = 'Promise.reject(new Error("tenant forgot to await"));';
  const url = URL.createObjectURL(new Blob([src], { type: "application/javascript" }));
  const worker = new Worker(url, { type: "module" });
  const message = await new Promise<string>((resolve) => onWorkerError(worker, resolve));
  worker.terminate();
  URL.revokeObjectURL(url);
  assert(/tenant forgot to await/.test(message), message);
  // Still alive and serving.
  await new Promise((r) => setTimeout(r, 200));
  assert(true);
});
