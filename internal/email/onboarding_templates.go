package email

// Phase C of the public-beta launch plan (docs/public-beta-launch-plan.md).
// Six onboarding drip templates, fired via the SendDripEmailJob River
// worker at day 0 / 2 / 4 / 6 / 8 / 10 after signup. Hardcoded Go
// const strings — no admin UI or DB rows for templates. See the
// plan for the reasoning (short answer: version-controlled, ships
// fast, iteration cadence is monthly at most).
//
// Every template shares the same layout: 600px inline-styled table,
// blue header, prose body, opt-out footer with the sovereignty
// tagline. Cloned shape from docs/emails/2026-07-06-beta-update.html
// (the last hand-written beta update) so all outbound platform mail
// has one visual identity.
//
// Data struct is intentionally small — anything that varies per
// user goes here and gets interpolated via html/template. New fields
// require a template touch too so they don't render as blanks.

import (
	"bytes"
	"html/template"
)

// OnboardingData is the interpolation context for every onboarding
// template. Fields that may be empty (DisplayName, ProjectName) are
// checked with `{{if .Field}}...{{end}}` inside each template body.
type OnboardingData struct {
	UserEmail      string
	DisplayName    string // optional; empty for users who haven't set one
	ProjectName    string // optional; empty when the user has no projects yet
	UnsubscribeURL string // HMAC-signed absolute URL — see BuildUnsubscribeURL
	DocsURL        string // deep link to the in-console docs
	ConsoleURL     string // marketing/console URL for CTA buttons
}

// RenderOnboardingStep renders template `step` (0..5) with `data`
// and returns the HTML body + subject. Returns an error if the step
// is out of range or the template execution fails (impossible in
// practice unless a data field is misspelled in a template).
//
// Caller passes the rendered body to EmailService.SendRaw. The
// worker never inspects the body — this is a single entry point so
// the templates + subjects are in one place.
func RenderOnboardingStep(step int, data OnboardingData) (subject, body string, err error) {
	if step < 0 || step >= len(onboardingTemplates) {
		return "", "", errInvalidOnboardingStep
	}
	tpl := onboardingTemplates[step]
	t, err := template.New("onboarding").Parse(tpl.body)
	if err != nil {
		return "", "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", "", err
	}
	return tpl.subject, buf.String(), nil
}

// NumOnboardingSteps is the length of the onboardingTemplates slice
// — exported so EnqueueOnboardingSeries knows how many River jobs
// to insert without importing this file's implementation detail.
const NumOnboardingSteps = 6

// onboardingIntervalDays is how many days between consecutive steps.
// Step N fires at signupTime + N*onboardingIntervalDays. Day 0
// (welcome) fires immediately (no delay).
const OnboardingIntervalDays = 2

// errInvalidOnboardingStep is returned when RenderOnboardingStep
// gets an out-of-range step number. Not exported — callers should
// consult NumOnboardingSteps before calling.
var errInvalidOnboardingStep = &onboardingError{"onboarding step out of range"}

type onboardingError struct{ msg string }

func (e *onboardingError) Error() string { return e.msg }

// onboardingTemplate is one row in the drip. Subject + body kept
// alongside each other so a copy edit touches one field pair.
type onboardingTemplate struct {
	subject string
	body    string
}

// onboardingTemplates is the ordered drip. Index = step number.
// Fields to update at the same time as the DRIP CONTENT lives:
//   - The plan's `docs/public-beta-launch-plan.md` Phase C table
//     lists the intended focus per day; keep in sync.
//   - Migration 000078_drip_email_sends CHECK constraint on
//     `step` column — currently allows any INT, so no schema
//     change on adding a step 6.
var onboardingTemplates = []onboardingTemplate{
	// Step 0 — Day 0 — Welcome
	{
		subject: "Welcome to Eurobase (beta)",
		body:    onboardingLayout(`
			<h2 style="margin:16px 0 4px; font-size:17px; color:#111827;">👋 Welcome to Eurobase</h2>
			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  {{if .DisplayName}}Hi {{.DisplayName}}!{{else}}Hi,{{end}}
			  Thanks for creating a Eurobase account. {{if .ProjectName}}Your project <strong>{{.ProjectName}}</strong> is live at <code style="background:#f3f4f6; padding:1px 5px; border-radius:4px; font-size:13px;">https://{{.ProjectName}}.eurobase.app</code>.{{else}}Your account is set up — create your first project when you're ready.{{end}}
			</p>
			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  Eurobase is EU-sovereign Backend-as-a-Service — Postgres, auth, storage, realtime, edge functions, all hosted on Scaleway (France). No US jurisdiction, no CLOUD Act.
			</p>
			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  Over the next ten days I'll send five short mails, one every couple of days, walking through what the platform does and where the interesting corners are. If you'd rather skip them, the unsubscribe link at the bottom takes you off the drip immediately (verification / password-reset mails you send to your own users are separate — those keep flowing).
			</p>
			<div style="background-color:#eff6ff; border:1px solid #bfdbfe; border-radius:8px; padding:16px 18px; margin:8px 0 14px;">
			  <p style="margin:0 0 6px; font-size:14px; font-weight:600; color:#1e3a8a;">Get started in five minutes</p>
			  <ul style="margin:0; padding-left:20px; font-size:14px; line-height:1.6; color:#1e40af;">
			    <li>Open <a href="{{.DocsURL}}" style="color:#1d4ed8;">/docs</a> — the in-console docs walk you through the SDK.</li>
			    <li>Copy your project's <code style="background:#e0e7ff; padding:1px 4px; border-radius:3px; font-size:12px;">public_key</code> from the console → SDK Keys.</li>
			    <li>Reply to this email if anything doesn't work. We read replies.</li>
			  </ul>
			</div>
			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  This is beta — features move, occasionally break, and the SLA is best-effort. If you catch something rough, tell us.
			</p>
		`),
	},

	// Step 1 — Day 2 — Row-Level Security
	{
		subject: "Row-Level Security in five minutes",
		body: onboardingLayout(`
			<h2 style="margin:16px 0 4px; font-size:17px; color:#111827;">🔒 Row-Level Security in five minutes</h2>
			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  You wired up auth in the wizard — nice. The piece that trips people up next is <strong>RLS policies</strong>: the Postgres feature that decides which rows each end-user can see.
			</p>
			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  Eurobase's convention: every table gets a policy that OR's in the service role, so your server-side code always works, and gates the user path on <code style="background:#f3f4f6; padding:1px 5px; border-radius:4px; font-size:13px;">auth_uid()</code>. Example for a per-owner <code style="background:#f3f4f6; padding:1px 5px; border-radius:4px; font-size:13px;">orders</code> table:
			</p>
			<pre style="background:#0f172a; color:#e2e8f0; padding:16px; border-radius:8px; font-size:12px; line-height:1.5; overflow-x:auto; margin:0 0 12px;">CREATE POLICY orders_owner ON orders
  FOR ALL
  USING (public.is_service_role() OR (user_id = auth_uid()))
  WITH CHECK (public.is_service_role() OR (user_id = auth_uid()));</pre>
			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  The full chapter is at <a href="{{.DocsURL}}#rls" style="color:#1d4ed8;">/docs → Row-Level Security</a>. Common mistakes it lists:
			</p>
			<ul style="margin:0 0 12px 18px; padding:0; font-size:14px; line-height:1.6; color:#374151;">
			  <li>Forgetting <code style="background:#f3f4f6; padding:1px 5px; border-radius:4px; font-size:13px;">WITH CHECK</code> — reads guarded, writes wide open.</li>
			  <li>Using <code style="background:#f3f4f6; padding:1px 5px; border-radius:4px; font-size:13px;">auth.uid()</code> from a Supabase habit — Eurobase's helper is <code style="background:#f3f4f6; padding:1px 5px; border-radius:4px; font-size:13px;">auth_uid()</code> (no dot).</li>
			  <li>Omitting the <code style="background:#f3f4f6; padding:1px 5px; border-radius:4px; font-size:13px;">is_service_role()</code> OR — your backend jobs then can't write.</li>
			</ul>
			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  Test any policy from the console: <em>Database → SQL editor</em> → run <code style="background:#f3f4f6; padding:1px 5px; border-radius:4px; font-size:13px;">SELECT * FROM orders</code> as a specific user via the API-key selector.
			</p>
		`),
	},

	// Step 2 — Day 4 — Storage + Realtime
	{
		subject: "Storage + Realtime: EU-hosted objects and live subscriptions",
		body: onboardingLayout(`
			<h2 style="margin:16px 0 4px; font-size:17px; color:#111827;">📦 Storage + Realtime</h2>
			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  Two features people often reach for after DB + auth: <strong>object storage</strong> and <strong>live subscriptions</strong>. Both are EU-hosted (Scaleway fr-par) and both are one SDK call.
			</p>

			<h3 style="margin:16px 0 6px; font-size:15px; color:#111827;">Storage — S3-compatible buckets</h3>
			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  Every project gets a bucket. Upload from the SDK:
			</p>
			<pre style="background:#0f172a; color:#e2e8f0; padding:16px; border-radius:8px; font-size:12px; line-height:1.5; overflow-x:auto; margin:0 0 12px;">await eb.storage.upload('avatars/alice.png', file, { contentType: 'image/png' })
const url = await eb.storage.signedUrl('avatars/alice.png', { expiresIn: 3600 })</pre>
			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  Files carry policies just like DB rows — same <code style="background:#f3f4f6; padding:1px 5px; border-radius:4px; font-size:13px;">auth_uid()</code>-shaped rules apply. Docs: <a href="{{.DocsURL}}#storage" style="color:#1d4ed8;">/docs → Storage</a>.
			</p>

			<h3 style="margin:16px 0 6px; font-size:15px; color:#111827;">Realtime — WebSocket subscriptions with row filters</h3>
			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  Subscribe to a table + filter server-side. The gateway checks the filter against your RLS policies before pushing — no need to over-fetch:
			</p>
			<pre style="background:#0f172a; color:#e2e8f0; padding:16px; border-radius:8px; font-size:12px; line-height:1.5; overflow-x:auto; margin:0 0 12px;">eb.realtime
  .from('messages', { filter: 'room_id=eq.42' })
  .on('INSERT', (msg) => console.log('new message', msg.record))
  .subscribe()</pre>
			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  50 concurrent connections on Free, 10 000 on Pro. Docs: <a href="{{.DocsURL}}#realtime" style="color:#1d4ed8;">/docs → Realtime</a>.
			</p>
		`),
	},

	// Step 3 — Day 6 — Edge Functions + Vault
	{
		subject: "Edge Functions + Vault: Deno handlers + AES-256 secrets",
		body: onboardingLayout(`
			<h2 style="margin:16px 0 4px; font-size:17px; color:#111827;">🔧 Edge Functions + 🛡 Vault</h2>
			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  Where server-side logic lives on Eurobase.
			</p>

			<h3 style="margin:16px 0 6px; font-size:15px; color:#111827;">Edge Functions — Deno runtime, EU-hosted</h3>
			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  Handler contract: <code style="background:#f3f4f6; padding:1px 5px; border-radius:4px; font-size:13px;">module.exports = async (req, ctx) => …</code>. The <code style="background:#f3f4f6; padding:1px 5px; border-radius:4px; font-size:13px;">ctx</code> gives you <code style="background:#f3f4f6; padding:1px 5px; border-radius:4px; font-size:13px;">ctx.db.sql()</code>, <code style="background:#f3f4f6; padding:1px 5px; border-radius:4px; font-size:13px;">ctx.storage</code>, and <code style="background:#f3f4f6; padding:1px 5px; border-radius:4px; font-size:13px;">ctx.env.SECRET_NAME</code> — no separate SDK client needed.
			</p>
			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  Deploy with the CLI: <code style="background:#f3f4f6; padding:1px 5px; border-radius:4px; font-size:13px;">eurobase functions deploy my-handler</code>. Trigger from an HTTP call, a cron schedule, or a DB row event.
			</p>

			<h3 style="margin:16px 0 6px; font-size:15px; color:#111827;">Vault — AES-256-GCM encrypted secrets</h3>
			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  Store API keys, webhook secrets, anything you don't want in your git repo:
			</p>
			<pre style="background:#0f172a; color:#e2e8f0; padding:16px; border-radius:8px; font-size:12px; line-height:1.5; overflow-x:auto; margin:0 0 12px;">await eb.vault.set('STRIPE_KEY', 'sk_live_...')
// Inside an edge function:
const key = ctx.env.STRIPE_KEY</pre>
			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  Encrypted at rest with a per-tenant key. Docs: <a href="{{.DocsURL}}#functions" style="color:#1d4ed8;">/docs → Edge Functions</a> and <a href="{{.DocsURL}}#vault" style="color:#1d4ed8;">/docs → Vault</a>.
			</p>
		`),
	},

	// Step 4 — Day 8 — CLI + MCP
	{
		subject: "CLI + MCP: your keyboard companion",
		body: onboardingLayout(`
			<h2 style="margin:16px 0 4px; font-size:17px; color:#111827;">⌨ CLI + 🤖 MCP</h2>
			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  Two tools that make Eurobase disappear into your normal workflow.
			</p>

			<h3 style="margin:16px 0 6px; font-size:15px; color:#111827;">The <code style="background:#f3f4f6; padding:1px 5px; border-radius:4px; font-size:13px;">eurobase</code> CLI</h3>
			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  50+ commands for projects, DB, storage, vault, functions, migrations. Install with <code style="background:#f3f4f6; padding:1px 5px; border-radius:4px; font-size:13px;">brew install eurobase/tap/eurobase</code> or grab the binary from GitHub Releases.
			</p>
			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  The one you'll use most: <code style="background:#f3f4f6; padding:1px 5px; border-radius:4px; font-size:13px;">eurobase migrations up</code> — applies every unapplied migration in <code style="background:#f3f4f6; padding:1px 5px; border-radius:4px; font-size:13px;">./migrations</code> in order. Version-controlled schema changes, replayed in CI on every environment.
			</p>

			<h3 style="margin:16px 0 6px; font-size:15px; color:#111827;">MCP server — for AI IDEs</h3>
			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  If you use Claude Code, Cursor, Codex, or Windsurf, wire the Eurobase MCP server. Your agent can list tables, run SELECTs, manage the vault, invoke functions — without leaving the chat window. Setup is one JSON snippet per IDE.
			</p>
			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  Docs: <a href="{{.DocsURL}}#cli" style="color:#1d4ed8;">/docs → CLI</a> and <a href="{{.DocsURL}}#mcp" style="color:#1d4ed8;">/docs → MCP</a>.
			</p>
		`),
	},

	// Step 5 — Day 10 — Compliance + what's next
	{
		subject: "Compliance + what's next",
		body: onboardingLayout(`
			<h2 style="margin:16px 0 4px; font-size:17px; color:#111827;">📋 Compliance — because you'll need it eventually</h2>
			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  When a real user emails "what do you have on me?" (GDPR Art. 15) or asks for their data on the way out (Art. 20), you'll want to hand them a complete export in one click — not spend a day writing custom SQL. Every Eurobase project gets:
			</p>
			<ul style="margin:0 0 12px 18px; padding:0; font-size:14px; line-height:1.6; color:#374151;">
			  <li><strong>Per-user DSAR export</strong> — grabs every row that references the user's ID + the auth record.</li>
			  <li><strong>Full-project export</strong> — all tables + auth manifest + storage manifest + audit log, as one zip.</li>
			  <li><strong>Audit log</strong> — every administrative action with actor, IP, timestamp.</li>
			  <li><strong>DPA + Article 30 record</strong> — auto-generated from the sub-processor registry.</li>
			</ul>
			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  The console flow is Pro; the underlying API is on every tier (DSAR is a legal obligation and we're not paywalling compliance). Docs: <a href="{{.DocsURL}}#compliance" style="color:#1d4ed8;">/docs → Compliance</a>.
			</p>

			<h2 style="margin:24px 0 4px; font-size:17px; color:#111827;">🚧 What's coming</h2>
			<ul style="margin:0 0 12px 18px; padding:0; font-size:14px; line-height:1.6; color:#374151;">
			  <li><strong>Supabase migration CLI</strong> — in testing. Move an existing Supabase project across in one flow.</li>
			  <li><strong>Team tier (€149/mo)</strong> — dedicated Postgres per project (direct <code style="background:#f3f4f6; padding:1px 5px; border-radius:4px; font-size:13px;">DATABASE_URL</code>), backups + PITR, SSO, RBAC, SOC 2. Coming later this year.</li>
			</ul>

			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  That's the last of the drip. If Eurobase is starting to feel small — you're hitting the Free-tier caps, or your project's real users need production guarantees — Pro is €19/mo per project, everything unlocks. If not, keep prototyping; Free stays.
			</p>
			<p style="margin:0 0 12px; font-size:14px; line-height:1.6; color:#374151;">
			  Reply to this mail with anything you'd want us to build. We read replies.
			</p>
		`),
	},
}

// onboardingLayout wraps the per-step body content in the shared
// 600 px table + header + footer. Kept as a Go string-format so a
// visual change to the layout is a single edit rather than six.
// The passed `body` is untrusted-looking but is a HARD-CODED const
// in every call site — safe against injection.
func onboardingLayout(body string) string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Eurobase</title>
</head>
<body style="margin:0; padding:0; background-color:#f3f4f6;">
  <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background-color:#f3f4f6; padding:24px 0;">
    <tr>
      <td align="center">
        <table role="presentation" width="600" cellpadding="0" cellspacing="0" style="max-width:600px; width:100%; background-color:#ffffff; border-radius:12px; overflow:hidden; font-family:-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif;">

          <tr>
            <td style="background-color:#1d4ed8; padding:28px 32px;">
              <p style="margin:0; font-size:22px; font-weight:700; color:#ffffff;">Eurobase</p>
              <p style="margin:6px 0 0; font-size:14px; color:#bfdbfe;">Getting started</p>
            </td>
          </tr>

          <tr>
            <td style="padding:32px 32px 8px;">` + body + `
            </td>
          </tr>

          <tr>
            <td style="padding:24px 32px 32px; border-top:1px solid #e5e7eb;">
              <p style="margin:0 0 8px; font-size:13px; line-height:1.6; color:#6b7280;">
                Made in Berlin, hosted in France. Everything Eurobase runs on stays in EU jurisdiction (Scaleway).
              </p>
              <p style="margin:0; font-size:12px; color:#9ca3af;">
                Sent to {{.UserEmail}} &middot;
                <a href="{{.UnsubscribeURL}}" style="color:#6b7280; text-decoration:underline;">Unsubscribe from onboarding</a>
                &middot; <a href="{{.ConsoleURL}}" style="color:#6b7280; text-decoration:underline;">Eurobase console</a>
              </p>
            </td>
          </tr>

        </table>
      </td>
    </tr>
  </table>
</body>
</html>`
}
