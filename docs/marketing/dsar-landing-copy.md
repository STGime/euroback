# Marketing Page Copy — Automated DSAR

> Closes #252 (part of #248). This file is a **deliverable** for the
> separate `eurobase` marketing repo. The actual landing-page edits
> live there; this file hands you ready-to-paste copy, SEO meta,
> structured-data snippets, and the keyword-targeting rationale so the
> marketing team doesn't have to re-derive any of it.

## Deployment instructions

1. Copy the relevant sections below into the corresponding files in the
   `eurobase` repo (typically `app/page.tsx` or `src/pages/index.astro`
   for the landing page, `app/sitemap.ts` for the sitemap).
2. Add a sitemap entry for the new section anchor.
3. Internal-link from the existing blog post
   <https://www.eurobase.app/blog/compliance-tab-dsar-ropa-audit-log>
   back to the new landing-page section (suggested anchor:
   `#automated-dsar`).
4. Re-deploy the marketing site.

## Landing page — DSAR section

Drop this in alongside the existing "Auth", "Database", "Storage"
feature sections. It introduces DSAR as a first-class capability.

```html
<section id="automated-dsar" class="py-20">
  <div class="mx-auto max-w-5xl px-6">

    <span class="inline-block rounded-full bg-eurobase-100 px-3 py-1 text-xs font-semibold text-eurobase-700">
      Pro feature
    </span>

    <h2 class="mt-4 text-3xl font-bold tracking-tight sm:text-4xl">
      Automated DSAR — one click, audit-trailed, EU-only.
    </h2>

    <p class="mt-4 text-lg text-gray-600 max-w-2xl">
      When a user emails "what do you have on me?" (Article 15) or a
      customer leaves and asks for their data (Article 20), Eurobase
      turns the answer into a one-click console export. No SQL to
      write each time. No middleware to maintain. The 30-day deadline
      stays statutory; the tooling stops being the bottleneck.
    </p>

    <ul class="mt-8 space-y-3">
      <li class="flex gap-3">
        <span class="text-eurobase-600 mt-1">✓</span>
        <span class="text-gray-700">
          <strong>Article 15 + Article 20 in one click.</strong>
          Per-user export (every row referencing their user_id) and
          full-project export (every table + auth + storage manifest)
          as a downloadable zip.
        </span>
      </li>
      <li class="flex gap-3">
        <span class="text-eurobase-600 mt-1">✓</span>
        <span class="text-gray-700">
          <strong>Audit log captures every request, completion, and failure</strong>
          — with actor email + IP. Your evidence trail is built in,
          not bolted on.
        </span>
      </li>
      <li class="flex gap-3">
        <span class="text-eurobase-600 mt-1">✓</span>
        <span class="text-gray-700">
          <strong>All bytes stay on Scaleway fr-par.</strong>
          Zero CLOUD Act exposure. Download links expire after 7 days.
        </span>
      </li>
      <li class="flex gap-3">
        <span class="text-eurobase-600 mt-1">✓</span>
        <span class="text-gray-700">
          <strong>API stays open on every tier.</strong>
          Free-tier projects can still meet a statutory deadline by
          calling the export endpoint directly — we won't paywall a
          legal obligation. The Pro tier saves you from writing the
          script.
        </span>
      </li>
    </ul>

    <div class="mt-10 flex flex-wrap items-center gap-4">
      <a href="/pricing"
         class="rounded-lg bg-eurobase-600 px-6 py-3 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700">
        See Pro pricing
      </a>
      <a href="/blog/compliance-tab-dsar-ropa-audit-log"
         class="text-sm font-semibold text-eurobase-700 underline">
        Read: How Eurobase ships RoPA, Audit Log + DSAR in one tab →
      </a>
    </div>

  </div>
</section>
```

## SEO meta

### `<head>` tags for the landing page

```html
<title>Eurobase — EU-sovereign Backend-as-a-Service with automated DSAR</title>
<meta name="description"
      content="Eurobase ships one-click GDPR Article 15 + 20 (DSAR) exports for every project. Audit-trailed, EU-only on Scaleway, no middleware. Free API. Pro one-click." />
<meta property="og:title"
      content="Automated DSAR exports for SaaS — Eurobase" />
<meta property="og:description"
      content="One-click GDPR Article 15 + 20 (DSAR) exports. EU-only, audit-trailed. Free API on every tier; Pro turns it into a click." />
<meta property="og:type" content="website" />
<meta property="og:url" content="https://www.eurobase.app/#automated-dsar" />
<link rel="canonical" href="https://www.eurobase.app/" />
```

### Dedicated DSAR-focused page (optional)

If the marketing team wants a separate `/features/dsar` page (better
for direct keyword targeting), use:

```html
<title>Automated DSAR for SaaS — GDPR Article 15 + 20 in one click | Eurobase</title>
<meta name="description"
      content="One-click DSAR exports built into every Eurobase project. Article 15 (subject access) and Article 20 (data portability). Audit-trailed, EU-sovereign on Scaleway, no middleware to maintain." />
```

## Schema.org structured data

Drop this `<script type="application/ld+json">` in the `<head>` of the
landing page (or the dedicated `/features/dsar` page). Helps the DSAR
feature get pulled into Google's rich-results panel.

```json
{
  "@context": "https://schema.org",
  "@type": "SoftwareApplication",
  "name": "Eurobase",
  "applicationCategory": "DeveloperApplication",
  "operatingSystem": "Web",
  "offers": [
    {
      "@type": "Offer",
      "name": "Free",
      "price": "0",
      "priceCurrency": "EUR"
    },
    {
      "@type": "Offer",
      "name": "Pro",
      "price": "19",
      "priceCurrency": "EUR",
      "priceSpecification": {
        "@type": "UnitPriceSpecification",
        "unitText": "MONTH",
        "billingDuration": "P1M"
      }
    }
  ],
  "featureList": [
    "EU-sovereign infrastructure (Scaleway, France)",
    "Postgres + Auth + Storage + Edge Functions",
    "Automated DSAR exports (GDPR Article 15 + 20)",
    "Article 30 records-of-processing report",
    "Tamper-evident audit log (hash chain)",
    "Per-project sub-processor registry",
    "BYO custom SMTP",
    "Per-project rate limits"
  ],
  "publisher": {
    "@type": "Organization",
    "name": "Eurobase",
    "url": "https://www.eurobase.app"
  }
}
```

## Keyword targets

Ranked by intent + competition. Aim the H1 + H2 + first paragraph at
the top three; sprinkle the rest naturally through the page.

### Primary (high intent, lower competition)

- **"automated DSAR SaaS"** — competitors don't offer this; we should
  rank quickly.
- **"GDPR Article 15 export API"** — developers searching with intent
  to integrate.
- **"DSAR tool EU sovereign"** — the privacy-engineering crowd.
- **"one-click DSAR GDPR"** — the user's own framing from the blog
  post; consistency helps both sides reinforce.

### Secondary (broader, higher competition)

- "Supabase alternative GDPR"
- "Firebase alternative EU"
- "Article 30 records of processing automated"
- "GDPR data subject access request automation"
- "EU backend as a service"

### Long-tail (cheap to capture)

- "how to fulfill a DSAR in a SaaS app"
- "GDPR Article 20 export tool"
- "SaaS GDPR audit log"
- "CLOUD Act free backend EU"

## Suggested internal links

- Blog post → new landing-page DSAR section
  (suggested anchor on the blog: insert
  `<a href="/#automated-dsar">→ See it on our landing page</a>`
  in the conclusion)
- `/pricing` → new landing-page DSAR section
  (the pricing page footer link to `/docs#compliance` is good; add
  one to `/#automated-dsar` in the Pro card)
- Sub-processor / sovereignty page (if it exists) → DSAR section

## Out of scope for this file

- Production-grade design system / component library wiring (use the
  marketing repo's existing primitives).
- A/B testing the copy variants.
- Translations (EN-only for now; DE / FR are tracked as separate
  marketing-team work).
- Pricing-page changes — those live in this repo's `console/src/routes/
  pricing/+page.svelte` and shipped in PR #254.

## Reference — what's already built

The DSAR capability is already shipped:

- **API** — `POST /platform/projects/{id}/compliance/exports` (full
  project, Article 20), `POST /platform/projects/{id}/compliance/exports/user`
  (single user, Article 15). Source:
  `internal/compliance/export.go` + `export_handler.go` in this repo.
- **Console** — Compliance → Data Export tab. Source:
  `console/src/routes/(app)/p/[id]/compliance/+page.svelte`.
- **Audit trail** — `audit.ActionExportRequested` /
  `audit.ActionExportCompleted` / `audit.ActionExportFailed`. Source:
  `internal/audit/service.go`.
- **Rate limit** — 1 export per data subject per 24h on the per-user
  endpoint. Source: `internal/compliance/export.go`.
- **Soft-gate** — Free tier sees an upgrade card; Pro sees the
  controls. The API stays callable on every tier. Source: PR #255 in
  this repo, docs at `docs/compliance/dsar-soft-gate.md`.

The marketing page just needs to point at all this with the right
keywords.
