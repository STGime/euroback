// Cache-key derivation for the runner's in-memory code cache.
//
// Closes #200: the cache was keyed by function id alone with a 5-minute
// TTL, so a redeploy kept serving stale code until the TTL expired. The
// gateway (and the worker's cron invoker) now send X-Function-Version —
// already looked up for the invoke — and the key includes it, so a
// version bump is a guaranteed cache miss and redeploys take effect
// immediately. Old-version entries age out via the existing LRU/TTL.
//
// The header is not HMAC-covered: a forged version can only cause a
// cache miss — code is always loaded from the DB by id. An absent or
// garbage version (older gateway during a rolling deploy) falls back to
// the id-only key, i.e. the pre-#200 TTL-bounded behaviour.
export function functionCacheKey(functionId: string, version: string | null): string {
  if (version && /^[0-9]+$/.test(version)) {
    return `${functionId}@v${version}`;
  }
  return functionId;
}
