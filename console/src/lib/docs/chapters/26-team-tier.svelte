<script lang="ts">
	import { goto } from '$app/navigation';
	// In the single-page docs this scrolled; each chapter is now its own
	// URL, so cross-references navigate instead.
	function scrollTo(id: string) {
		goto(`/docs/${id}`);
	}
</script>

		<section id="team-tier" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">26. Team tier <span class="text-sm font-normal text-emerald-700">(closed beta)</span></h2>
			<p class="text-sm italic text-gray-500 mb-4">
				Alex's first pilot law firm signs on. LexVault needs production isolation, daily backups, and a way to safely try schema changes without holding their breath.
			</p>

			<div class="rounded-md bg-amber-50 border border-amber-200 p-3 text-xs text-amber-900 mb-4">
				<strong>Closed beta — free during the beta window.</strong> Team is not yet self-serve on the pricing page.
				Email <a href="mailto:contact@eurobase.app" class="underline hover:no-underline">contact@eurobase.app</a>
				with a one-line description of the workload; grants are manual and we'll notify you 30 days before
				billing turns on.
			</div>

			<div class="rounded-xl border border-emerald-200 bg-emerald-50/50 p-5 mb-4 space-y-2">
				<h3 class="text-base font-semibold text-gray-900">How Team billing works <span class="text-xs font-normal text-emerald-800">(post-beta)</span></h3>
				<p class="text-sm text-gray-700">
					A Team subscription is priced <strong>per organisation</strong>, not per project. First release: one org per user.
				</p>
				<ul class="list-disc pl-5 text-sm text-gray-700 space-y-1">
					<li><strong>€149/mo base</strong> per org — SSO (OIDC), invites, RBAC, priority support, SLA, and <em>one bundled Team-tier project</em> with dedicated Postgres.</li>
					<li>Attach additional projects to the org at their per-project runtime rate:
						<ul class="list-disc pl-5 mt-1 space-y-0.5 text-gray-600">
							<li><strong>Pro project — €25/mo</strong> (shared cluster). 5 GB DB, 50 GB storage, 100k MAU, 1k rps. Right choice for most internal tooling.</li>
							<li><strong>Free project — €0</strong>. For staging, scratch, demos. Free-tier limits apply.</li>
							<li><strong>Extra Team project — €89/mo</strong> (dedicated PG). Only when a specific app genuinely needs its own Postgres.</li>
						</ul>
					</li>
					<li><strong>Sample bills.</strong> €149 + 3×€25 = <strong>€224/mo</strong> (1 Team + 3 Pro). €149 + 5×€25 = <strong>€274/mo</strong> (org-only + Pro projects). €149 + €89 + €25 = <strong>€263/mo</strong> (2 Team + 1 Pro).</li>
				</ul>
				<p class="text-xs text-gray-600">
					Multi-org (each with its own SSO provider) is on the roadmap; first release limits each user to one org they own.
					Under the hood each project is a separate Mollie subscription line — you'll see one line per project on your bill rather than a single "base + extras" invoice. Unifying that is a UX cleanup we've scoped.
				</p>
			</div>

			<div class="rounded-xl border border-gray-200 bg-white p-6 space-y-4">
				<h3 class="text-base font-semibold text-gray-900">What Team unlocks (beyond Pro)</h3>
				<ul class="list-disc pl-5 text-sm text-gray-700 space-y-1.5">
					<li><strong>Dedicated Postgres instance</strong> in Scaleway fr-par — no shared cluster, no noisy neighbours. Each Team project is its own managed-PG instance.</li>
					<li><strong>Automatic daily backups</strong> with 7-day retention, managed by Scaleway.</li>
					<li><strong>On-demand snapshots</strong> with an optional tag — take one before a risky migration, restore in a few clicks. Retention window shared with daily backups.</li>
					<li><strong>Self-serve restore</strong> from any snapshot, no support ticket. 1 restore per calendar month is included.</li>
					<li><strong>Organizations + OIDC SSO</strong> so a company can share access under one identity provider — covered in the next chapter.</li>
					<li><strong>Direct Postgres connection URL</strong> — the DSN reveal from chapter <button onclick={() => scrollTo('connect-db')} class="text-eurobase-700 hover:underline cursor-pointer">25</button>.</li>
				</ul>

				<div class="rounded-md bg-blue-50 border border-blue-200 p-3 text-xs text-blue-900">
					<strong>What's NOT in the beta yet.</strong> Say this up front to your own team so nobody plans around it:
					SAML SSO (OIDC only for now), project-level RBAC (Owner/Admin/Developer/Read-only),
					domain-based SSO auto-provisioning (invitees must be added by email today),
					dev/staging/prod branches, preview function envs. PITR was removed in favour of snapshots.
				</div>

				<h3 class="text-base font-semibold text-gray-900 pt-2">1. Requesting beta access</h3>
				<ol class="list-decimal pl-5 text-sm text-gray-700 space-y-2">
					<li>
						Alex emails <a href="mailto:contact@eurobase.app" class="text-eurobase-700 hover:underline">contact@eurobase.app</a>
						from the same address they signed up with — one line about the workload
						(<em>"early-access pilot with a Berlin law firm, ~500 documents, GDPR-sensitive"</em>) is enough.
					</li>
					<li>
						Ops flips <code class="rounded bg-gray-100 px-1 text-[11px]">team_beta_access</code> on Alex's account
						(there's a small superadmin panel for this — no direct DB writes needed on our side).
					</li>
					<li>
						On next page load, Alex sees three new affordances in the console:
						<ul class="list-disc pl-5 mt-1 space-y-0.5">
							<li>A <strong>Team</strong> plan option in the new-project modal.</li>
							<li>An <strong>Organizations</strong> entry in the sidebar.</li>
							<li>A <strong>Support</strong> entry in the sidebar — Team-tier tickets are prioritised.</li>
						</ul>
					</li>
				</ol>

				<h3 class="text-base font-semibold text-gray-900 pt-2">2. Creating a Team project</h3>
				<ol class="list-decimal pl-5 text-sm text-gray-700 space-y-2">
					<li>
						<strong>Projects → New project</strong>. Pick plan <strong>Team</strong>. Region defaults to
						<code class="rounded bg-gray-100 px-1 text-[11px]">fr-par</code> (Scaleway EU).
					</li>
					<li>
						Submit. Behind the scenes we enqueue a provisioning job that calls Scaleway to spin up a
						dedicated managed-PG instance, waits for it to hit <code class="rounded bg-gray-100 px-1 text-[11px]">state=active</code>,
						sets the backup schedule, and bootstraps the SDK runtime role.
					</li>
					<li>
						Wall-clock: <strong>90 seconds to 4 minutes</strong> typically; 15 minutes is the ceiling before the worker gives up.
					</li>
				</ol>

				<div class="rounded-md bg-amber-50 border border-amber-200 p-3 text-xs text-amber-900">
					<strong>No progress bar yet</strong> — the project sits in state <code class="rounded bg-amber-100 px-1">provisioning</code> until the worker flips it to <code class="rounded bg-amber-100 px-1">active</code>. Refresh the projects page to see the flip; if it's been over 15 minutes, open a support ticket.
				</div>

				<h3 class="text-base font-semibold text-gray-900 pt-2">3. Backups + snapshots</h3>
				<p class="text-sm text-gray-700">
					Alex's Team project is live. Before the first schema migration for the pilot, they want a rollback point.
				</p>
				<ol class="list-decimal pl-5 text-sm text-gray-700 space-y-2">
					<li>
						<strong>Project → Database → Backups</strong>. The list shows daily backups + any manual snapshots,
						with created-at, size, and an optional tag column.
					</li>
					<li>
						Click <strong>Create Snapshot</strong>. Optional tag (max 64 chars) — Alex types
						<code class="rounded bg-gray-100 px-1 text-[11px]">pre-migration-cases-v2</code> so future-Alex knows what this was for.
					</li>
					<li>
						Snapshot lands in a few seconds. Alex runs the migration; something breaks.
					</li>
					<li>
						On the snapshot row, click <strong>Restore</strong> → confirm modal → a progress panel takes over.
						Restore quota badge shows the current usage; 1 restore per calendar month is included, with the
						console blocking further self-serve restores after that (contact support for extras during the beta).
					</li>
				</ol>

				<div class="rounded-md bg-emerald-50 border border-emerald-200 p-3 text-xs text-emerald-900">
					<strong>Rollback semantics.</strong> Restore rewrites the tenant schema back to the snapshot state.
					Any data written after the snapshot is lost — the point of the snapshot is that Alex chose that moment.
					Storage objects in the project's S3 bucket are <strong>not</strong> included; those have their own
					lifecycle and can be handled via Legal Team's retention holds if you need them locked (chapter
					<button onclick={() => scrollTo('legal-tech')} class="text-eurobase-700 hover:underline cursor-pointer">28</button>).
				</div>

				<h3 class="text-base font-semibold text-gray-900 pt-2">4. Connecting external tools</h3>
				<p class="text-sm text-gray-700">
					Team also unlocks the direct <code class="rounded bg-gray-100 px-1 text-[11px]">DATABASE_URL</code> surface
					so Prisma / Drizzle / Payload / <code class="rounded bg-gray-100 px-1 text-[11px]">psql</code> / migration
					runners can connect straight to the dedicated instance. Chapter
					<button onclick={() => scrollTo('connect-db')} class="text-eurobase-700 hover:underline cursor-pointer">25</button>
					walks that path in detail.
				</p>

				<h3 class="text-base font-semibold text-gray-900 pt-2">5. Known rough edges (beta)</h3>
				<ul class="list-disc pl-5 text-sm text-gray-700 space-y-1">
					<li>No progress indicator during provisioning; refresh to check.</li>
					<li>No self-serve billing yet — the Mollie plan-change wiring lands separately.</li>
					<li>Downgrade Team → Free/Pro is not implemented. Once a project is on a dedicated instance, it stays there for the beta.</li>
				</ul>
			</div>

			<div class="mt-4">
				<button onclick={() => scrollTo('orgs-sso')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Organizations &amp; SSO (OIDC) &rarr;
				</button>
			</div>
		</section>
