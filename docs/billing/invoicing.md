# Invoice PDFs (PR 6)

Every paid invoice gets a PDF rendered asynchronously, uploaded
to Scaleway Object Storage, and pointed at by
`invoices.pdf_object_key`. The console downloads via a presigned
URL redirect.

## Content

Estonian invoice-minimum for a non-VAT-registered SaaS:

- **Seller.** Eurobase OÜ, Ahtri 12, Tallinn 15551, registry
  code 17557586, contact@eurobase.app.
- **Buyer.** Owner email + display name (billing profile is a
  future feature — no full billing address today).
- **Invoice number.** `EB-XXXXXXXX` — uppercase first-8-hex of
  the invoice UUID. Short enough to quote over the phone.
- **Dates.** Issued (invoice `created_at`) + Paid (invoice
  `paid_at` if set). Zero-value `paid_at` elides the "Paid" line
  entirely rather than showing `0001-01-01`.
- **Line item.** `Eurobase <plan> subscription — <project>`,
  period from `subscriptions.started_at` → `next_charge_at`,
  amount `invoices.amount_cents` formatted as `€X.XX`.
- **Total.** Same amount (single line, no VAT).
- **VAT statement.** `Not VAT-registered under Estonian VAT
  Act §19 (below the €40,000 taxable-turnover threshold).`

Money format is locale-neutral: `€19.00` (dot decimal, no
thousand separator, symbol prefix). Deliberate — German
`1.900,00` vs UK `1,900.00` has caused real support tickets on
other EU platforms.

## Rendering

`internal/billing/pdf.go` — pure-Go PDF generation via
`github.com/go-pdf/fpdf`. No Chrome, no wkhtmltopdf binary, no
CGO. Single-page A4, ~800 bytes to ~3 KB per invoice.

Trade-off: visual polish ceiling is lower than a
Chrome-headless approach. For a one-line subscription invoice
that's fine; if we ever need multi-line invoices with tables
that need HTML/CSS layout, revisit.

**Known font limitation:** Helvetica (fpdf core font) is Latin-1
only. Cyrillic / Greek / CJK buyer names or project names will
corrupt PDFs. Not blocking today (all closed-beta users are
Latin-script) but will bite the first Bulgarian / Greek / Asian
customer. Fix requires embedding a Unicode TTF (~1 MB per font);
tracked as a follow-up.

## Storage

Bucket: `eurobase-platform-invoices` (Scaleway fr-par, private).

Key format: `invoices/<first-2-hex>/<uuid>.pdf`. The 2-char
prefix fans invoices out across up to 256 S3 hash partitions —
irrelevant at current volume, matters once N crosses ~100k.

Created once per environment via
`deploy/scripts/create-invoices-bucket.sh` (S3-compatible; uses
AWS CLI with a Scaleway endpoint override).

Uploads use the existing `storage.S3Client` (same client as
tenant storage — no extra credentials). Content-Type is
`application/pdf`.

## Async render + on-demand fallback

Two paths write `pdf_object_key`:

1. **Async, best-effort.** PR 4's webhook enqueues a goroutine
   after the paid-transition tx commits. 30-second timeout,
   detached context (Mollie's HTTP client may cancel the
   incoming request after our 200; we don't want that to abort
   the render). Failures log `billing.invoice.render_failed`
   but don't roll back the payment state.

2. **On-demand, blocking.** If the download endpoint receives a
   request for an invoice with `pdf_object_key` still NULL, it
   renders + uploads inline before the presign step. Ensures
   the user gets a PDF even if the async path failed or hasn't
   completed yet.

The renderer is deterministic on inputs — a second render of
the same invoice produces identical bytes and overwrites the
same S3 key. Idempotent.

## Download flow

```
GET /platform/billing/invoices/{id}/pdf
        │
        ▼
Auth check → invoice ownership check
        │
        ▼
pdf_object_key populated? ──no──► render + upload inline
        │yes                          │
        ▼                              ▼
GeneratePresignedDownloadURLAs (5m TTL, suggested filename)
        │
        ▼
302 Found → browser fetches PDF from Scaleway directly
```

5-minute TTL is long enough for a browser fetch, short enough
that a leaked URL isn't a lasting problem.

## Endpoints

| Method | Path | Purpose |
|---|---|---|
| GET | `/platform/billing/invoices` | List every invoice for every project the caller owns. |
| GET | `/platform/billing/invoices/{id}/pdf` | 302 to a presigned S3 URL. |

Both auth'd via the platform middleware. Both 503 when
`BILLING_ENABLED=false`.

List response shape:

```json
{
  "invoices": [
    {
      "id": "…",
      "number": "EB-…",
      "project_id": "…",
      "project_name": "MyProject",
      "created_at": "2026-08-01T12:00:00Z",
      "paid_at": "2026-08-01T12:05:00Z",
      "amount_cents": 1900,
      "currency": "EUR",
      "status": "paid",
      "has_pdf": true
    }
  ]
}
```

## Test coverage

Pool-less unit tests:
- `formatEUR` locale-neutrality (Pro/Team/zero/refund/USD-fallback).
- `shortInvoiceNumber` phone-friendly shape.
- `RenderInvoicePDF` produces valid PDF bytes (magic + trailer).
- Unpaid variant renders differently from paid (different code path).

Not covered by unit tests (same rationale as PR 4/5): DB JOIN
in `loadInvoiceData`, S3 upload, download-handler DB queries.
CI runs `go test` without a Postgres service or Scaleway
credentials. Exercised via manual staging QA.

## Manual QA

1. Trigger a Mollie test-mode paid payment (Pro checkout, "select
   outcome" → Paid).
2. Verify `invoices` row appears with `status='paid'`. Wait ~5s
   for async render.
3. Verify `pdf_object_key` is populated. Verify the object
   exists in Scaleway (console or `aws s3 ls`).
4. `curl -L -H "Authorization: Bearer $TOKEN" \
   https://api.eurobase.app/platform/billing/invoices/{id}/pdf
   > invoice.pdf` — opens in Preview.
5. Verify the PDF contains: Eurobase OÜ, Ahtri 12 Tallinn 15551,
   registry 17557586, project name, €19.00, VAT §19 statement.
