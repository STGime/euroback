		<section id="compliance" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">17. Compliance, Audit Log &amp; DSAR</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex's client asks for proof that their data stays in the EU and a trail of who changed what — and a user emails LexVault asking "what do you have on me?" Both answered without leaving the console.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					The Compliance page has three tabs: <strong>DPA Report</strong>, <strong>Audit Log</strong>, and <strong>Data Export</strong>. Together they cover the three things a GDPR auditor (or your customer's legal team) actually asks for: the Article 30 records-of-processing report, the tamper-evident trail of who changed what, and one-click <strong>DSAR</strong> exports for both the "tell me everything about me" (Article 15) and the "give me my data so I can leave" (Article 20) shapes.
				</p>

				<div class="rounded-lg border border-eurobase-200 bg-eurobase-50/50 px-4 py-3">
					<p class="text-xs font-semibold text-eurobase-900 mb-1">What is a DSAR?</p>
					<p class="text-xs text-eurobase-900 leading-relaxed">
						A <strong>Data Subject Access Request</strong> is the legal mechanism a user or a regulator uses to ask "what personal data do you hold on me?" (Article 15) or "give me my data in a portable format so I can move to a competitor" (Article 20). The controller (you) has a 30-day deadline to comply. Most teams build the export pipeline ad-hoc the first time one lands &mdash; Eurobase ships it as a one-click console flow + an API endpoint so you don't write the SQL each time.
					</p>
				</div>

				<h3 class="text-lg font-semibold text-gray-900">DPA Report</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Generates a Data Processing Agreement (Article 30) report showing your sub-processors, data flow, encryption status, and whether any CLOUD Act exposure exists. Download it as JSON for your compliance records.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Audit Log</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Every sensitive action on your project is automatically recorded in the audit log with a timestamp, actor email, IP address, and metadata. Tracked actions include:
				</p>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>Auth config changes</strong> &mdash; updating login providers, OAuth settings, session duration</li>
					<li><strong>API key regeneration</strong> &mdash; who rotated keys and when</li>
					<li><strong>Project deletion</strong> &mdash; logged before the data is removed</li>
					<li><strong>Schema DDL</strong> &mdash; creating, dropping, or renaming tables and columns</li>
					<li><strong>RLS policy changes</strong> &mdash; toggling row-level security, applying presets, creating or dropping policies</li>
					<li><strong>Index changes</strong> &mdash; creating or dropping indexes and constraints</li>
					<li><strong>OAuth secrets</strong> &mdash; setting or rotating provider client secrets (the secret itself is never logged, only the event)</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Filtering</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Use the action filter dropdown to narrow the log to a specific action type &mdash; for example, show only <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">schema.drop_table</code> events to trace who deleted a table and when.
				</p>

				<div class="rounded-lg border border-blue-200 bg-blue-50 px-4 py-3 flex gap-3">
					<svg class="h-5 w-5 text-blue-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<p class="text-sm text-blue-800">
						Audit log entries cannot be edited or deleted. They are append-only and stored in the platform database, separate from your project's tenant schema.
					</p>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Data Export &mdash; one-click DSAR</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					The Data Export tab handles both DSAR shapes regulators and end-users actually ask for: Article 15 (single user, "tell me everything you have on me") and Article 20 (full project, "give me the data so I can leave"). Both run in the background and produce a downloadable zip; the download link expires after 7 days, and all the bytes stay on EU infrastructure (Scaleway fr-par) the whole way. Nothing to wire up, no SQL to write &mdash; the deadline is statutory, the tooling shouldn't be the bottleneck.
				</p>

				<h4 class="text-sm font-semibold text-gray-900 mt-3">Full Project Export &mdash; Article 20 (data portability)</h4>
				<p class="text-sm text-gray-700 leading-relaxed">
					Exports every table in your tenant schema (up to 100,000 rows per table) including the auth <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">users</code> table, plus the project audit log (latest 10,000 entries) and a <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">_metadata.json</code> describing the run, as a single zip. Uploaded files in storage are <strong>not</strong> in the archive &mdash; fetch those through the storage API. Pick <strong>JSON</strong> for round-trippable structure or <strong>CSV</strong> for spreadsheets. The one-click button in this tab is Pro and above; on Free the same export is available through the API (next section). Use this when a customer hands you "we're leaving, give us our data" or when a regulator asks for a full snapshot.
				</p>

				<h4 class="text-sm font-semibold text-gray-900 mt-3">Single-User Export &mdash; Article 15 (subject access request)</h4>
				<p class="text-sm text-gray-700 leading-relaxed">
					Search by email or paste a user UUID; the export contains only that user's auth record and every row in your tenant tables that references their <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">user_id</code>. Rate-limited to one export per <em>data subject</em> per 24 hours (so any admin calling for that user is gated by the same window) &mdash; this stops a runaway script or a hostile actor with a leaked admin token from exfiltrating the user table one row at a time.
				</p>

				<h4 class="text-sm font-semibold text-gray-900 mt-3">Scripting exports &mdash; nightly backups from the API</h4>
				<p class="text-sm text-gray-700 leading-relaxed">
					Both exports are plain platform-API calls, so a cron job on your side can run them on every tier, including Free. The project's public and service keys cannot call them: they are <em>platform</em> endpoints on <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">api.eurobase.app</code>, authenticated as <em>you</em>. Create a <a href="/docs/account" class="text-eurobase-600 hover:underline cursor-pointer">Personal Access Token</a> (Account &rarr; Personal Access Tokens, starts with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eb_pat_</code>) and send it as a Bearer token. The token owner must be an <strong>admin</strong> of the project.
				</p>
				<div class="rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto mt-2">
					<pre># 1. request a full export (json or csv)
curl -X POST https://api.eurobase.app/platform/projects/$PROJECT_ID/compliance/export \
  -H "Authorization: Bearer $EUROBASE_PAT" -H "Content-Type: application/json" \
  -d '{'{'}"format":"json"{'}'}'
# &rarr; 202 {'{'}"id":"&lt;exportId&gt;","status":"pending",...{'}'}

# 2. poll until status is "completed", then download
curl https://api.eurobase.app/platform/projects/$PROJECT_ID/compliance/exports/$EXPORT_ID \
  -H "Authorization: Bearer $EUROBASE_PAT"
# &rarr; {'{'}"status":"completed","download_url":"https://...","expires_at":"..."{'}'}</pre>
				</div>
				<ul class="text-sm text-gray-700 leading-relaxed list-disc pl-5 mt-2 space-y-1">
					<li><code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">download_url</code> is a presigned link valid for <strong>1 hour</strong>; request the status again for a fresh one. The archive itself is kept for <strong>7 days</strong>, then deleted.</li>
					<li>Rate limit: <strong>one full-project export per hour</strong> per project (single-user exports: one per data subject per 24 h). A nightly job fits comfortably.</li>
					<li>Export calls go through the platform API, so they do <strong>not</strong> count as project activity for the Free-tier idle pause (see <a href="/docs/create-project" class="text-eurobase-600 hover:underline cursor-pointer">chapter 2</a>). A paused project still exports fine.</li>
					<li>Every request, completion and failure lands in the Audit Log with the token owner's email and IP.</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Your Data Processing Agreement (Art. 28 GDPR)</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					The DPA report above is a description of processing (Article 30). The <em>contract</em> that makes Eurobase your processor is the <strong>Data Processing Agreement v2</strong>, which you accepted click-through when you created your account &mdash; that is a valid written form under Art. 28(9), and it applies on every tier, including Free. The full text is at <a href="/legal/dpa" class="text-eurobase-600 hover:underline cursor-pointer">/legal/dpa</a>: it names the contracting entity (Eurobase OÜ, Ahtri 12, Tallinn 15551, Estonia, registry code 17557586), sets out the technical and organisational measures, and carries the sub-processor annex. Print it or save it as PDF for your records; the sub-processors actually engaged by <em>your</em> project's enabled features are listed live in the DPA Report tab.
				</p>
				<p class="text-sm text-gray-700 leading-relaxed">
					Need a <strong>countersigned copy</strong> on Eurobase letterhead with the sub-processor list attached, for example because your auditor or Verein board wants a wet-ink or PDF-signed document? That is a paid-tier service: available on Pro and above on request via <a href="mailto:dpo@eurobase.app" class="text-eurobase-600 hover:underline">dpo@eurobase.app</a>. On Free, the click-through DPA at /legal/dpa is your contract.
				</p>

				<div class="rounded-lg border border-gray-200 bg-gray-50 px-4 py-3 mt-3">
					<p class="text-xs font-semibold text-gray-700 mb-1.5">What the audit log records</p>
					<p class="text-xs text-gray-600 leading-relaxed">
						Every export request, completion, and failure is recorded in the Audit Log with the actor's email + IP. So the trail of who exported what, when, and why is always there for the next compliance review &mdash; even after the export bytes themselves expire.
					</p>
				</div>
			</div>

			<div class="mt-6 text-right">
				<a href="/docs/settings" class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Project Settings &rarr;
				</a>
			</div>
		</section>
