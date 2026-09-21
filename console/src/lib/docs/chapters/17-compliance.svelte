<script lang="ts">
	import { goto } from '$app/navigation';
	// In the single-page docs this scrolled; each chapter is now its own
	// URL, so cross-references navigate instead.
	function scrollTo(id: string) {
		goto(`/docs/${id}`);
	}
</script>

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
					Exports every row of every table in your tenant schema, plus the auth user records, storage object manifest, and audit log, as a single zip. Pick <strong>JSON</strong> for round-trippable structure or <strong>CSV</strong> for spreadsheets. Use this when a customer hands you "we're leaving, give us our data" or when a regulator asks for a full snapshot.
				</p>

				<h4 class="text-sm font-semibold text-gray-900 mt-3">Single-User Export &mdash; Article 15 (subject access request)</h4>
				<p class="text-sm text-gray-700 leading-relaxed">
					Search by email or paste a user UUID; the export contains only that user's auth record and every row in your tenant tables that references their <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">user_id</code>. Rate-limited to one export per <em>data subject</em> per 24 hours (so any admin calling for that user is gated by the same window) &mdash; this stops a runaway script or a hostile actor with a leaked admin token from exfiltrating the user table one row at a time.
				</p>

				<div class="rounded-lg border border-gray-200 bg-gray-50 px-4 py-3 mt-3">
					<p class="text-xs font-semibold text-gray-700 mb-1.5">What the audit log records</p>
					<p class="text-xs text-gray-600 leading-relaxed">
						Every export request, completion, and failure is recorded in the Audit Log with the actor's email + IP. So the trail of who exported what, when, and why is always there for the next compliance review &mdash; even after the export bytes themselves expire.
					</p>
				</div>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('settings')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Project Settings &rarr;
				</button>
			</div>
		</section>
