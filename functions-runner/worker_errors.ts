/**
 * Routes a Worker's uncaught errors (incl. unhandled rejections in the
 * tenant's code) to `onError` and stops them from propagating. Without
 * preventDefault(), Deno re-raises them in the parent as "Unhandled error
 * in child worker" and the whole runner exits — one tenant's
 * fire-and-forget rejection would fail every tenant's invocations.
 */
export function onWorkerError(worker: Worker, onError: (message: string) => void): void {
  worker.addEventListener("error", (e: ErrorEvent) => {
    e.preventDefault();
    onError(e.message);
  });
}
