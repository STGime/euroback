<script lang="ts">
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	const ORIGIN = 'https://console.eurobase.app';

	const TRANSFER_LABELS: Record<string, string> = {
		none: 'None — EU only',
		adequacy: 'Adequacy decision',
		SCCs: 'Standard Contractual Clauses',
		'SCCs+TIA': 'SCCs + transfer impact assessment',
		DPF: 'EU-US Data Privacy Framework',
		binding_corporate_rules: 'Binding Corporate Rules'
	};

	function transferLabel(m: string): string {
		return TRANSFER_LABELS[m] ?? m;
	}

	function addedOn(iso: string): string {
		const d = new Date(iso);
		return Number.isNaN(d.getTime()) ? '' : d.toISOString().slice(0, 10);
	}

	// Group by legal entity so a provider used for several services
	// (Scaleway: hosting, Postgres, storage, email…) reads as one row
	// block instead of five near-duplicates.
	let grouped = $derived.by(() => {
		const map = new Map<string, typeof data.processors>();
		for (const p of data.processors) {
			const list = map.get(p.legal_entity) ?? [];
			list.push(p);
			map.set(p.legal_entity, list);
		}
		return Array.from(map.values());
	});
</script>

<svelte:head>
	<title>Sub-processors — Eurobase</title>
	<meta
		name="description"
		content="The current list of third parties authorised to process customer data on behalf of Eurobase OÜ: legal entity, country, role, transfer mechanism and certifications. Referenced by the Eurobase Data Processing Agreement, Annex 3."
	/>
	<link rel="canonical" href="{ORIGIN}/legal/sub-processors" />
</svelte:head>

<div class="mx-auto max-w-4xl px-6 py-12">
	<nav class="mb-6 flex flex-wrap gap-x-4 gap-y-1 text-sm" aria-label="Legal documents">
		<a href="/legal/dpa" class="text-eurobase-600 hover:text-eurobase-700 underline">Data Processing Agreement</a>
		<a href="/legal/terms" class="text-eurobase-600 hover:text-eurobase-700 underline">Terms of Service</a>
		<a href="/legal/privacy" class="text-eurobase-600 hover:text-eurobase-700 underline">Privacy Policy</a>
		<span class="text-gray-900 font-medium" aria-current="page">Sub-processors</span>
	</nav>

	<div class="mb-6 border-b border-gray-200 pb-4">
		<h1 class="text-3xl font-semibold text-gray-900">Authorised sub-processors</h1>
		<p class="mt-2 text-sm text-gray-600 leading-relaxed">
			Third parties that process customer data on behalf of <strong>Eurobase OÜ</strong> (Ahtri 12, Tallinn 15551, Estonia, registry code 17557586).
			This is the list referenced in Section 7 and Annex 3 of the
			<a href="/legal/dpa" class="text-eurobase-600 hover:text-eurobase-700 underline">Data Processing Agreement</a>.
			It is rendered live from the platform's sub-processor registry — the same source that produces every project's Article 30 report — and we give at least 30 days' notice before adding or replacing an entry.
			Providers marked <em>when enabled</em> are only engaged for a project once you switch on the corresponding feature; your project's Compliance tab shows exactly which of these apply to you.
		</p>
	</div>

	{#if data.unavailable}
		<div class="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900">
			The live registry is not reachable right now. The list as of the DPA's effective date is printed in
			<a href="/legal/dpa" class="underline">Annex 3 of the Data Processing Agreement</a>; please try again in a moment or write to
			<a href="mailto:dpo@eurobase.app" class="underline">dpo@eurobase.app</a>.
		</div>
	{:else if data.processors.length === 0}
		<p class="text-sm text-gray-600">No sub-processors are currently registered.</p>
	{:else}
		<div class="overflow-x-auto rounded-lg border border-gray-200">
			<table class="min-w-full divide-y divide-gray-200 text-sm">
				<thead class="bg-gray-50 text-left text-xs font-semibold uppercase tracking-wider text-gray-600">
					<tr>
						<th scope="col" class="px-4 py-3">Sub-processor</th>
						<th scope="col" class="px-4 py-3">Country</th>
						<th scope="col" class="px-4 py-3">Service &amp; purpose</th>
						<th scope="col" class="px-4 py-3">Transfer basis</th>
						<th scope="col" class="px-4 py-3">Certifications</th>
						<th scope="col" class="px-4 py-3">Since</th>
					</tr>
				</thead>
				<tbody class="divide-y divide-gray-100 bg-white">
					{#each grouped as rows (rows[0].legal_entity)}
						{#each rows as p, i (p.id)}
							<tr>
								{#if i === 0}
									<td class="px-4 py-3 align-top" rowspan={rows.length}>
										<div class="font-medium text-gray-900">{p.legal_entity}</div>
										{#if p.name !== p.legal_entity}
											<div class="text-xs text-gray-500">{p.name}</div>
										{/if}
										{#if p.cloud_act_risk}
											<span class="mt-1 inline-flex items-center rounded-full bg-amber-50 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wider text-amber-700 ring-1 ring-inset ring-amber-200">US parent · CLOUD Act reach</span>
										{:else}
											<span class="mt-1 inline-flex items-center rounded-full bg-emerald-50 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wider text-emerald-700 ring-1 ring-inset ring-emerald-200">EU jurisdiction</span>
										{/if}
										<div class="mt-1 flex flex-wrap gap-x-2 text-xs">
											{#if p.dpa_url}<a href={p.dpa_url} rel="noopener noreferrer" target="_blank" class="text-eurobase-600 hover:underline">Their DPA</a>{/if}
											{#if p.privacy_url}<a href={p.privacy_url} rel="noopener noreferrer" target="_blank" class="text-eurobase-600 hover:underline">Privacy</a>{/if}
										</div>
									</td>
									<td class="px-4 py-3 align-top text-gray-700" rowspan={rows.length}>{p.country}</td>
								{/if}
								<td class="px-4 py-3 align-top text-gray-700">
									<div class="font-medium text-gray-900">{p.service}</div>
									<div class="text-xs text-gray-500">{p.purpose}</div>
									{#if p.data_categories?.length}
										<div class="mt-1 text-xs text-gray-500">Data: {p.data_categories.join(', ')}</div>
									{/if}
								</td>
								<td class="px-4 py-3 align-top text-gray-700">{transferLabel(p.transfer_mechanism)}</td>
								<td class="px-4 py-3 align-top text-gray-700">{p.security_certs?.length ? p.security_certs.join(', ') : '—'}</td>
								<td class="px-4 py-3 align-top text-gray-500 tabular-nums">{addedOn(p.added_at)}</td>
							</tr>
						{/each}
					{/each}
				</tbody>
			</table>
		</div>
	{/if}

	<p class="mt-8 text-xs text-gray-500">
		Machine-readable copy: <code class="rounded bg-gray-100 px-1 py-0.5">GET /platform/public/sub-processors</code> on the API.
		Questions about this list? Contact <a href="mailto:dpo@eurobase.app" class="text-eurobase-600 hover:text-eurobase-700 underline">dpo@eurobase.app</a>.
	</p>
</div>
