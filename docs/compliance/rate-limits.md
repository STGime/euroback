# Per-Project Rate Limits

> Umbrella issue [#224](https://github.com/STGime/euroback/issues/224). Mirrors
> Supabase's published Rate Limits page so the UX is familiar; the numbers
> and the enforcement architecture are described below. Console UI:
> **Auth → Rate Limits** tab on `/p/{id}/auth`.

## What it is

A small set of per-project knobs that decide how much load any one IP (or
the whole project) can put on the platform's auth and notification
pipelines. They exist to safeguard:

1. **The platform**, against burst abuse from one project tanking
   shared infrastructure (Scaleway TEM sender reputation, GatewayAPI
   spend, the auth pool's RPS budget).
2. **The project owner**, against runaway costs from a
   misconfigured client or a leaked anon key.
3. **End users**, against brute-force on signup, sign-in, token
   refresh, and OTP verify.

The numbers are tuned to give legitimate apps room to breathe and
hostile traffic a wall to hit. Project owners can tighten or loosen
each knob from the console; the underlying limiter uses the per-tenant
shape so two projects on the same Redis can never collide.

## Defaults

These are the values returned by `DefaultRateLimits()` in
`internal/tenant/auth_config.go`. Empty fields in the console form
fall back to these. If you change one in code, update this table.

| Knob | Default | Unit | Notes |
|---|---|---|---|
| `signup_signin_per_5min_per_ip` | **8** | requests / 5 min / IP | Held below Supabase's 30 until `emails_per_hour` enforcement lands (#235); the lower interim caps the verification-email amplification path. Bumps to 30 then. |
| `token_refresh_per_5min_per_ip` | **150** | requests / 5 min / IP | High because legitimate SDK clients refresh proactively. |
| `token_verification_per_5min_per_ip` | **30** | requests / 5 min / IP | Covers OTP, magic-link verify, email verify, reset. 6-digit OTP brute-force defence sits on top of (and depends on) #233. |
| `emails_per_hour` | **2** | sends / hour / project | **Defined but not yet enforced.** Parked behind BYO-SMTP (#235) — the platform's single-shared-SMTP model would silently break confirmation flows under the 2/h floor. The field in `auth_config.rate_limits` saves but no code path consumes it today. |
| `sms_per_hour` | **30** | sends / hour / project | Enforced. Over-quota sends are silently skipped with a `slog.Warn` operator signal; the phone-OTP handler still returns 200 so a 429 can't reveal phone-membership. |
| `trust_proxy` | **false** | bool | Whether the limiter trusts the leftmost `X-Forwarded-For`. See [IP source — `trust_proxy`](#ip-source--trust_proxy) for the trade-off. |

## How limits are enforced

The same `RateLimiter.Allow` primitive (Redis INCR + EXPIRE via a Lua
script, `internal/ratelimit/limiter.go`) is used everywhere. The
difference between knobs is the **key shape** and the **window**.

### Per-IP auth gates (signup/signin, token refresh, token verify)

Key shape:

```
auth:{action}:project:{projectID}:{identifier}
```

Where `action` is one of `signup_signin`, `token_refresh`,
`token_verify`, and `identifier` is the value returned by
`ratelimit.ClientIPForProject(r, trustProxy)`. Counter resets at the
end of its 5-minute window.

Distinct from the legacy / platform-wide

```
auth:{action}:{identifier}
```

key still used by per-email anti-brute-force gates (signin failures,
forgot-password, magic-link, resend-verify, phone OTP). Those gates
are NOT exposed on the Rate Limits page — they're security floors,
not knobs — and they stay at platform defaults regardless of what a
project sets here.

### Per-project hourly quotas (email, SMS)

Key shape:

```
quota:{email|sms}:project:{projectID}
```

Window: 1 hour, TTL-managed by Redis. One counter per project per
action; checked **before** the underlying provider call so an
over-quota send never reaches TEM / GatewayAPI.

Email enforcement is dormant today (see [`emails_per_hour`](#defaults))
but the key is reserved.

### Fail-open behaviour

When Redis is unreachable (dev without Redis, transient outage), every
rate-limit check returns "allowed" with no error. The rationale: a
quota outage shouldn't take auth offline platform-wide, and the
upstream provider quotas (TEM, GatewayAPI, the gateway pool) are a
hard backstop. The Discord `#alerts` channel pages on Redis
unreachability via the cluster health monitor; rate-limit decisions
just stop applying until the limiter recovers.

## IP source — `trust_proxy`

The most consequential knob. Same word means different things in
different deployments and the right answer depends on the chain
between the client and the gateway.

### Mechanics

- **`trust_proxy = true`** — read leftmost `X-Forwarded-For`; fall
  back to the TCP peer if XFF is absent.
- **`trust_proxy = false`** — read TCP peer (`r.RemoteAddr`); XFF is
  ignored entirely.

### The two failure modes

Picking the wrong value breaks the limiter in one of two ways.

**`true` under header forgery — security-critical.** If anything in
the chain *appends* to XFF instead of overwriting it (typical with
nginx-ingress `use-forwarded-headers: true`, sometimes needed to
recover the client IP through a load balancer), the leftmost entry
is **client-controlled**. An attacker rotating the header bypasses
every per-IP gate — signup spam, OTP brute-force, token-refresh
flooding.

**`false` behind one shared hop — limiter collapse.** In a deployment
where every request hits the gateway through one nginx pod (Eurobase
prod), `r.RemoteAddr` is the same value for every request. With
`SignupSigninPer5MinPerIP=8`, a 9-person office team can't all sign
up in the same 5 minutes — the "per-IP" gate becomes a "per-project
total" gate.

### Eurobase default: `false` (pending #238 verification)

`#238` tracks the empirical verification of what XFF the gateway
actually receives in prod (`curl -H 'X-Forwarded-For: 1.2.3.4'`
against the gateway, then check `data_access_log` for the recorded
IP). Until that's confirmed, the default is the safe side: `false`
keeps the limiter resistant to header forgery at the cost of the
per-IP discrimination collapsing to per-project total. Project owners
who know their deployment trusts XFF can flip the toggle in the
console today; the precondition `#238` will document for the default
to flip is roughly:

1. nginx-ingress `use-forwarded-headers: false` (the default; we
   ship no override).
2. Scaleway LB delivering the real client IP via PROXY protocol OR
   `externalTrafficPolicy: Local` on the nginx-ingress Service.

If both hold, leftmost XFF is the real client IP and the safe default
is `true`. The long-term hardening is a trusted-hop-count / known-proxy-
CIDR strategy that's robust to either failure mode — that's also
tracked in `#238`.

## Debugging — Redis snippets

All commands assume a `redis-cli` connection scoped to the prod
limiter Redis. Replace `<projectID>` with the tenant UUID.

### "Is this IP currently capped?"

```sh
# Counter for the signup/signin per-IP gate
redis-cli get "auth:signup_signin:project:<projectID>:1.2.3.4"
# TTL (seconds until the window resets)
redis-cli ttl "auth:signup_signin:project:<projectID>:1.2.3.4"
```

If the value is at or above the configured limit and the TTL is > 0,
the next request from `1.2.3.4` to this project's signup or signin
will return 429.

### "Where did the project's hourly SMS budget go?"

```sh
# Current hour's count
redis-cli get "quota:sms:project:<projectID>"
# When does the bucket reset
redis-cli ttl "quota:sms:project:<projectID>"
```

If the value equals `auth_config.rate_limits.sms_per_hour` (or the
default 30) and the TTL is > 0, the next OTP send for the project
silently skips with a `slog.Warn` line.

### "Force a counter reset"

```sh
# Burn the per-IP signup counter for this project + IP
redis-cli del "auth:signup_signin:project:<projectID>:1.2.3.4"
```

Resets the budget immediately. Use during incident response when a
legitimate caller is collateral damage from a misset cap.

## Override precedence

1. **Explicit project override** — the value persisted in
   `auth_config.rate_limits.{knob}` via the console / API.
2. **Platform default** — `DefaultRateLimits()` in
   `internal/tenant/auth_config.go`, the values in this doc's
   defaults table.

`EffectiveRateLimits()` (`internal/tenant/auth_config.go`) does the
merge: zero / absent numeric fields fall through to the platform
default; `trust_proxy *bool` distinguishes "explicit `false`" from
"absent" so a project that explicitly opted out stays opted out
even if the platform default flips.

There is **no per-plan tier** today — the same defaults apply to
Free and Pro projects. Once the Pro tier ships (Mollie billing,
#163), this doc gets a third precedence column.

## Related

- `#224` — umbrella issue.
- `#225` foundation: schema, helper, signup+signin gates.
- `#226` token refresh + token verification gates.
- `#227` / PR #234: SMS hourly quota; email half deferred behind
  `#235`.
- `#228`: `trust_proxy` enforcement.
- `#229`: console UI (Auth → Rate Limits tab).
- `#233`: phone-OTP code-only verify SQL fix (the gates here narrow
  but don't close the underlying issue).
- `#235`: BYO-SMTP + paid TEM (unblocks email quotas).
- `#238`: XFF chain verification + trusted-hop-count hardening
  (unblocks the `trust_proxy` default flip).
