# Audit-log export (SIEM integration)

Eurobase forwards every `public.audit_log` event on your project to a
sink of your choice — webhook or syslog — so your security team gets the
same immutable stream in real time that we retain platform-side for
10 years under S3 Object Lock (#352).

Legal-Team tier only. Configure destinations in Console → **Compliance
→ Retention → SIEM export**, or via the API under
`/platform/projects/{id}/compliance/audit-export`.

- **Webhook** (this doc): HMAC-signed POST to your https endpoint.
- **Syslog**: RFC 5424 over TCP/TLS. See #355 (in progress).

## Delivery guarantees

- **At-least-once.** Every event with `seq > last_cursor` is delivered.
  On any non-2xx or timeout, `last_cursor` stays put and the next tick
  retries from the same point. Bounded retry via River's exponential
  backoff (5 attempts before terminal). Sinks must dedupe on
  `events[].id` if they care about exactly-once.
- **Cursor starts at 0 on registration**, and the deliverer
  fast-forwards past pre-registration history rather than back-filling.
  Existing events sit in the platform WORM archive (#352) — talk to
  support for a one-off export if you need them.
- **Batch size**: 100 events per POST (env-tunable). A slow sink that
  can't handle 100/POST should return non-2xx; the deliverer holds
  the cursor and retries.
- **Ordering**: events within a batch are `seq ASC`; batches are
  monotonic in `seq`.

## POST envelope

```
POST /your/endpoint HTTP/1.1
Content-Type: application/json
X-Eurobase-Timestamp: 1786134214
X-Eurobase-Signature: sha256=8f2a...

{
  "events": [
    {
      "id": "...",
      "project_id": "...",
      "actor_id": "...",
      "actor_email": "user@example.com",
      "action": "project.deleted",
      "target_type": "project",
      "target_id": "proj_abc",
      "metadata": {"reason": "customer_request"},
      "ip_address": "203.0.113.42",
      "created_at": "2026-08-10T12:34:56Z",
      "seq": 1000042,
      "row_hash": "abcdef..."
    }
  ],
  "cursor": 1000042,
  "delivered_at": "2026-08-10T12:35:00Z"
}
```

Sinks MUST respond with **HTTP 2xx** to acknowledge receipt. Any
other status (including 3xx redirects, which the deliverer refuses
to follow — SSRF prevention) holds the cursor for the next attempt.

## Signature verification

The signature protects both authenticity (only holders of the vault
secret can produce it) and freshness (the timestamp is inside the
HMAC input, so a captured request cannot be replayed with a new
timestamp).

**Canonical input**: `<timestamp>` + `.` + `<raw request body>`

**Algorithm**: `HMAC-SHA256(secret, canonical)` → hex-encoded, prefixed
with `sha256=`.

**Verification checklist** (do all four):

1. Extract `X-Eurobase-Signature` and `X-Eurobase-Timestamp`.
2. Reject if `|now - X-Eurobase-Timestamp| > 300s` (5-minute
   replay window).
3. Recompute HMAC against the **exact raw body** you received —
   don't re-serialise the JSON; key ordering and whitespace matter.
4. `hmac.Equal` (constant-time) against the received signature.

Any of steps 1–4 failing → respond with a non-2xx status; the
deliverer will retry.

### Unauthenticated destinations

If you registered the destination with an empty `secret_ref`, the
deliverer **omits** both the signature and timestamp headers
entirely (an HMAC with a known/empty key is a checksum, not a
signature, so we don't pretend otherwise). The console labels such
destinations as **unauthenticated** — the sink cannot verify the
stream came from Eurobase.

### Verification examples

Each example expects two inputs:
- `secret` — the raw signing key (whatever you stored in the vault).
- `body` — the exact raw request body bytes.

#### Go

```go
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

func verify(secret, body []byte, sigHeader, tsHeader string) error {
	ts, err := strconv.ParseInt(tsHeader, 10, 64)
	if err != nil {
		return err
	}
	if abs(time.Now().Unix()-ts) > 300 {
		return errors.New("timestamp outside 5-minute window")
	}
	want := "sha256=" + hexHMAC(secret, tsHeader+"."+string(body))
	if !hmac.Equal([]byte(want), []byte(sigHeader)) {
		return errors.New("signature mismatch")
	}
	return nil
}

func hexHMAC(key []byte, msg string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
```

#### Python

```python
import hmac
import hashlib
import time

def verify(secret: bytes, body: bytes, sig_header: str, ts_header: str) -> None:
    ts = int(ts_header)
    if abs(int(time.time()) - ts) > 300:
        raise ValueError("timestamp outside 5-minute window")
    mac = hmac.new(secret, f"{ts_header}.".encode() + body, hashlib.sha256).hexdigest()
    want = f"sha256={mac}"
    if not hmac.compare_digest(want, sig_header):
        raise ValueError("signature mismatch")
```

#### Node.js

```javascript
const crypto = require('crypto');

function verify(secret, body, sigHeader, tsHeader) {
  const ts = parseInt(tsHeader, 10);
  if (Math.abs(Date.now() / 1000 - ts) > 300) {
    throw new Error('timestamp outside 5-minute window');
  }
  const mac = crypto
    .createHmac('sha256', secret)
    .update(`${tsHeader}.`)
    .update(body)
    .digest('hex');
  const want = `sha256=${mac}`;
  if (!crypto.timingSafeEqual(Buffer.from(want), Buffer.from(sigHeader))) {
    throw new Error('signature mismatch');
  }
}
```

## Endpoint restrictions

The API rejects destination endpoints that would enable SSRF into
our cluster:

- Plaintext `http://` (webhook must use `https://`).
- Loopback (`127.0.0.1`, `::1`), `localhost`.
- RFC 1918 (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`).
- IPv6 ULA (`fc00::/7`), link-local (`fe80::/10`, includes cloud
  metadata IPs like `169.254.169.254`).
- CGNAT (`100.64.0.0/10`).
- Unspecified (`0.0.0.0`, `::`), multicast.

The deliverer re-checks the resolved IP at connect time so DNS
rebinding (register `attacker.com` → resolves to `10.0.0.1` later)
also fails.

## Testing your sink

Console UI: Compliance → Retention → SIEM export → **Test** button
on the row. Fires a synthetic `audit_export.test` event at your
endpoint with `"test": true` in the envelope root so your sink can
skip it if it cares.

API: `POST /platform/projects/{id}/compliance/audit-export/{destID}/test`
returns `{"delivered": true, "status_code": <sink status>}` on 2xx,
or `{"code": "delivery_failed", "status_code": ..., "detail": "..."}`
on non-2xx.
