<script lang="ts">
	import { goto } from '$app/navigation';
	// In the single-page docs this scrolled; each chapter is now its own
	// URL, so cross-references navigate instead.
	function scrollTo(id: string) {
		goto(`/docs/${id}`);
	}
</script>

		<section id="legal-tech" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">28. German legal-tech retention <span class="text-sm font-normal text-emerald-700">(Legal Team)</span></h2>
			<p class="text-sm italic text-gray-500 mb-4">
				Per-prefix WORM policies and row/object-scoped retention holds for tenants subject to §50 BRAO, §257 HGB, or §147 AO.
			</p>

			<div class="rounded-md bg-amber-50 border border-amber-200 p-3 text-xs text-amber-900 mb-4">
				<strong>Closed beta — invite only.</strong> Legal Team is a separate SKU on top of Team; not self-serve yet.
				Email <a href="mailto:contact@eurobase.app" class="underline hover:no-underline">contact@eurobase.app</a> with your
				retention basis (BRAO / HGB / AO / other) and workload. Grants are manual during the beta window.
			</div>

			<div class="rounded-xl border border-gray-200 bg-white p-6 space-y-4">
				<h3 class="text-base font-semibold text-gray-900">Why this exists</h3>
				<p class="text-sm text-gray-700">
					German professional-services firms — law firms in particular — have to keep specific record classes for
					years under statute, and cannot delete them on a DSAR erasure request during that window. The three
					common bases:
				</p>
				<ul class="list-disc pl-5 text-sm text-gray-700 space-y-1">
					<li><strong>§50 BRAO</strong> (Federal Lawyers' Act) — client files retained <strong>6 years</strong> from case end.</li>
					<li><strong>§257 HGB</strong> (Commercial Code) — split by record class: books, inventories, opening balance sheets, annual accounts, and invoices (<em>Buchungsbelege</em>) retained <strong>10 years</strong>; received / sent commercial letters (<em>Handelsbriefe</em>) retained <strong>6 years</strong>.</li>
					<li><strong>§147 AO</strong> (Fiscal Code) — same split, mirroring §257 HGB: <strong>10 years</strong> for books and accounting records, <strong>6 years</strong> for other tax-relevant business correspondence.</li>
				</ul>
				<p class="text-sm text-gray-700">
					On Free / Pro / plain Team, retention is defence-in-depth (soft delete + audit log) but not statutorily
					enforced. Legal Team makes retention <strong>WORM-enforced at the storage layer</strong> — even a compromised
					admin key can't delete a locked object before its retention date. That's what German auditors and clients
					subject to <code class="rounded bg-gray-100 px-1 text-[11px]">MaRisk AT 7.2</code> want to see.
				</p>

				<h3 class="text-base font-semibold text-gray-900 pt-2">What Legal Team unlocks</h3>
				<ul class="list-disc pl-5 text-sm text-gray-700 space-y-2">
					<li>
						<strong>Default per-prefix policies.</strong> Every object under <code class="rounded bg-gray-100 px-1 text-[11px]">/invoices/*</code>
						is retained 10 years under §257 HGB, WORM-enforced by S3 Object Lock. Additional
						prefixes (<code class="rounded bg-gray-100 px-1 text-[11px]">/client-files/*</code> for §50 BRAO,
						<code class="rounded bg-gray-100 px-1 text-[11px]">/tax/*</code> for §147 AO) configured per project.
					</li>
					<li>
						<strong>Ad-hoc retention holds.</strong> When a customer cites a legal basis mid-lifetime
						(e.g. "this row / this object is subject to litigation hold"), the console's Retention tab lets
						you pin the specific row, object, or table beyond its default policy. The hold survives DSAR erasure
						attempts and is audited.
					</li>
					<li>
						<strong>Honest DSAR erasure.</strong> When a user asks to be forgotten, held items are
						<em>refused</em> with a specific message the requester sees in their export:
						<em>"retained under §257 HGB, purgeable after 2036-03-14"</em>. No silent no-op; no ambiguous
						"we removed everything we could". The exporter enumerates every held item + basis + earliest purge
						date so the user can plan a follow-up request.
					</li>
					<li>
						<strong>10-year audit-log retention</strong> (vs 90 days on standard tiers) so the audit trail
						itself survives the same statutory window as the data it describes.
					</li>
				</ul>

				<h3 class="text-base font-semibold text-gray-900 pt-2">Where to find it in the console</h3>
				<p class="text-sm text-gray-700">
					<strong>Project → Compliance → Retention</strong>. The tab shows every currently-held row/object with its
					basis + expiry, an audit-log filter for retention actions, and a link to the upgrade CTA if you're not
					on Legal Team yet. DSAR erasure requests fired from the DSAR tab automatically respect any active holds.
				</p>

				<h3 class="text-base font-semibold text-gray-900 pt-2">Related</h3>
				<ul class="list-disc pl-5 text-sm text-gray-700 space-y-1">
					<li>Compliance overview → section <button onclick={() => scrollTo('compliance')} class="text-eurobase-700 hover:underline cursor-pointer">17</button> (DPA report, sub-processors, DSAR baseline).</li>
					<li>Direct Postgres connection → section <button onclick={() => scrollTo('connect-db')} class="text-eurobase-700 hover:underline cursor-pointer">25</button> (Legal Team includes the dedicated instance from the Team tier).</li>
				</ul>
			</div>

			<div class="mt-4">
				<button onclick={() => scrollTo('next')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: What's Next &rarr;
				</button>
			</div>
		</section>
