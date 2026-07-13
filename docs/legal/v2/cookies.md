<!--
REVIEWER NOTES — read before publication (v2)

  1. This notice reflects an audit of the codebase as of v2.0
     (re-verified during the public-beta launch Phase A):
       - 0 Set-Cookie calls in the Go backend.
       - localStorage keys: eurobase_token, eurobase_email,
         SQL editor tabs/history.
       - 1 third-party preconnect: fonts.bunny.net (EU, no cookies).
       - No analytics, no marketing pixels, no tracking.
     If any of those change, update this page in the SAME PR that
     introduces the change.
  2. What changed vs v1: nothing substantive. Version bump kept for
     consistency with the rest of the v2 set. Cookies notice is
     jurisdiction-neutral — the Estonia governing-law pivot in Terms
     doesn't touch this page.
  3. Verdict: no consent banner required today. Keep this verdict
     under review every time anything is added that touches storage
     or third-party scripts.
  4. Lawyer review recommended but not blocking — content is factual.
-->

# Cookie & Storage Notice

**Version 2.0 — effective {{EFFECTIVE_DATE}}**

This page tells you exactly what Eurobase stores in your browser and what data leaves your device while you use eurobase.app. We have built the platform to be friendly to your privacy by default: **no analytics, no marketing pixels, no third-party tracking, and no cookies set by Eurobase.**

## Cookies

The Eurobase console does **not** set any HTTP cookies. Authentication uses a JSON Web Token kept in your browser's `localStorage` and sent in the `Authorization` header — not as a cookie.

## Browser storage we use

We use a small number of `localStorage` keys. None are transmitted to anyone other than Eurobase, and only the auth token is sent at all.

| Key | Purpose | Strictly necessary? |
|---|---|---|
| `eurobase_token` | Your signed-in session JWT. Sent to the Eurobase API in the `Authorization` header. Without it you cannot use the console. | Yes |
| `eurobase_email` | Lets the console show your email in the header without an extra round-trip. | Yes (UX of the strictly necessary login) |
| `eurobase:tabs:<projectId>` | Open tabs in the SQL editor for a given project. Stays on your device. | No, but device-local convenience only |
| `eurobase:history:<projectId>` | Recent SQL queries you ran in the editor (last 50). Stays on your device. | No, but device-local convenience only |

You can clear all of these at any time through your browser's developer tools or by signing out and clearing site data.

## Third-party services that load when you open the console

| Service | Where | What it loads | Cookies it sets | Why it is here |
|---|---|---|---|---|
| Bunny Fonts (Bunny Font Delivery GmbH, Austria) | `fonts.bunny.net` | The Inter font family | None | EU-hosted, privacy-friendly font CDN. We do not use Google Fonts. |

Bunny Fonts sees the IP address that requests the stylesheet (a normal property of any web request). It does not set cookies and does not build user profiles; their privacy policy is at https://fonts.bunny.net/about.

## Things we do not do

- We do not load Google Analytics, Google Tag Manager, Google Fonts, reCAPTCHA, or any other Google service.
- We do not load any social-media pixel (Meta, LinkedIn, Twitter, Reddit, TikTok).
- We do not embed third-party widgets (Calendly, Hotjar, Intercom, Drift, etc.).
- We do not use session-replay or heatmap tools.
- We do not fingerprint you by canvas, fonts, or device characteristics.

## Why no cookie banner

Under the ePrivacy Directive (Art. 5(3)) consent is required for storage that is **not strictly necessary** for the service the user has explicitly asked for, or for any analytics/tracking. Today none of those apply: the only data in your browser is what you need to stay signed in plus the editor's open tabs. We therefore do not show a consent banner, but we do disclose all of this here so you have full visibility (Art. 13 GDPR).

## What would change this

If we ever add any of the following, we will update this page **and** add a clear consent mechanism before activating them:

- Analytics or product-usage measurement (PostHog, Plausible, Matomo, Mixpanel, Sentry session-replay, etc.).
- Marketing or advertising pixels.
- Third-party widgets that set cookies (Stripe.js / Mollie.js when in-page checkout launches; embedded video; chat).
- A/B testing tools that store an identifier per visitor.

## How to ask questions or complain

If you think this notice is inaccurate or misses something, please email **dpo@eurobase.app**. You can also lodge a complaint with your data-protection authority — see the **Privacy Policy** at /legal/privacy for details.

## Changelog

- v1.0 ({{EFFECTIVE_DATE}}) — Initial publication.
