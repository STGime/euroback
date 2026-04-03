<script lang="ts">
	import { page } from '$app/stores';
	import { api, type DPAReport } from '$lib/api.js';
	import { onMount } from 'svelte';

	let projectId = $derived($page.params.id);

	let report: DPAReport | null = $state(null);
	let loading = $state(true);
	let error: string | null = $state(null);

	onMount(() => { loadReport(); });

	async function loadReport() {
		loading = true;
		error = null;
		try {
			report = await api.getDPAReport(projectId);
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load compliance report';
		} finally {
			loading = false;
		}
	}

	function downloadJSON() {
		if (!report) return;
		const blob = new Blob([JSON.stringify(report, null, 2)], { type: 'application/json' });
		const url = URL.createObjectURL(blob);
		const a = document.createElement('a');
		a.href = url;
		a.download = `dpa-report-${report.customer.project_slug}-${new Date().toISOString().slice(0, 10)}.json`;
		document.body.appendChild(a);
		a.click();
		document.body.removeChild(a);
		URL.revokeObjectURL(url);
	}

	const countryFlags: Record<string, string> = {
		FR: '\u{1F1EB}\u{1F1F7}',
		NL: '\u{1F1F3}\u{1F1F1}',
		DE: '\u{1F1E9}\u{1F1EA}',
		US: '\u{1F1FA}\u{1F1F8}',
	};

	function getFlag(code: string): string {
		return countryFlags[code] || '';
	}

	function formatDate(iso: string): string {
		return new Date(iso).toLocaleString('en-GB', {
			year: 'numeric', month: 'short', day: 'numeric',
			hour: '2-digit', minute: '2-digit', timeZoneName: 'short'
		});
	}

	function formatCategories(cats: string[]): string {
		return cats.map(c => c.replace(/_/g, ' ')).join(', ');
	}

	let hasCloudActProviders = $derived(
		report?.sub_processors?.some(sp => sp.cloud_act_risk) ?? false
	);
</script>

<div class="space-y-6">
	<!-- Header -->
	<div class="flex items-center justify-between">
		<div>
			<h2 class="text-lg font-semibold text-gray-900">GDPR Compliance Report</h2>
			{#if report}
				<p class="mt-1 text-sm text-gray-500">
					Generated {formatDate(report.generated_at)} &middot; Version {report.version}
				</p>
			{/if}
		</div>
		{#if report}
			<button
				onclick={downloadJSON}
				class="cursor-pointer inline-flex items-center gap-2 rounded-md bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 transition-colors"
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" d="M3 16.5v2.25A2.25 2.25 0 0 0 5.25 21h13.5A2.25 2.25 0 0 0 21 18.75V16.5M16.5 12 12 16.5m0 0L7.5 12m4.5 4.5V3" />
				</svg>
				Download JSON Report
			</button>
		{/if}
	</div>

	{#if loading}
		<div class="space-y-4">
			<div class="h-32 animate-pulse rounded-lg bg-gray-100"></div>
			<div class="h-64 animate-pulse rounded-lg bg-gray-100"></div>
		</div>
	{:else if error}
		<div class="rounded-lg border border-red-200 bg-red-50 px-4 py-3">
			<p class="text-sm text-red-700">{error}</p>
			<button onclick={loadReport} class="cursor-pointer mt-2 text-sm font-medium text-red-600 hover:text-red-800">Retry</button>
		</div>
	{:else if report}
		<!-- CLOUD Act Warning -->
		{#if hasCloudActProviders}
			<div class="rounded-lg border border-amber-300 bg-amber-50 px-4 py-3">
				<div class="flex items-start gap-3">
					<svg class="mt-0.5 h-5 w-5 shrink-0 text-amber-500" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z" />
					</svg>
					<div>
						<h3 class="text-sm font-semibold text-amber-800">US CLOUD Act Exposure Detected</h3>
						<p class="mt-1 text-sm text-amber-700">{report.summary.cloud_act_details}</p>
					</div>
				</div>
			</div>
		{/if}

		<!-- Summary Card -->
		<div class="grid grid-cols-1 gap-4 sm:grid-cols-3">
			<div class="rounded-lg border border-gray-200 bg-white p-4">
				<p class="text-sm text-gray-500">Sub-Processors</p>
				<p class="mt-1 text-2xl font-bold text-gray-900">{report.summary.total_sub_processors}</p>
			</div>
			<div class="rounded-lg border border-gray-200 bg-white p-4">
				<p class="text-sm text-gray-500">Data Sovereignty</p>
				<div class="mt-1 flex items-center gap-2">
					{#if report.summary.eu_only}
						<span class="inline-flex items-center rounded-full bg-green-100 px-2.5 py-0.5 text-xs font-medium text-green-700">EU Only</span>
					{:else}
						<span class="inline-flex items-center rounded-full bg-amber-100 px-2.5 py-0.5 text-xs font-medium text-amber-700">Cross-border</span>
					{/if}
				</div>
			</div>
			<div class="rounded-lg border border-gray-200 bg-white p-4">
				<p class="text-sm text-gray-500">CLOUD Act Exposure</p>
				<div class="mt-1 flex items-center gap-2">
					{#if report.summary.cloud_act_exposure}
						<span class="inline-flex items-center rounded-full bg-red-100 px-2.5 py-0.5 text-xs font-medium text-red-700">Exposed</span>
					{:else}
						<span class="inline-flex items-center rounded-full bg-green-100 px-2.5 py-0.5 text-xs font-medium text-green-700">None</span>
					{/if}
				</div>
			</div>
		</div>

		<!-- Sub-Processor Table -->
		<div class="rounded-lg border border-gray-200 bg-white">
			<div class="border-b border-gray-200 px-4 py-3">
				<h3 class="text-sm font-semibold text-gray-900">Active Sub-Processors</h3>
			</div>
			<div class="overflow-x-auto">
				<table class="min-w-full divide-y divide-gray-200">
					<thead class="bg-gray-50">
						<tr>
							<th class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Name</th>
							<th class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Country</th>
							<th class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Purpose</th>
							<th class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Data Categories</th>
							<th class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500">CLOUD Act</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-gray-200">
						{#each report.sub_processors as sp}
							<tr class="hover:bg-gray-50">
								<td class="whitespace-nowrap px-4 py-3 text-sm font-medium text-gray-900">
									{sp.name}
									{#if sp.security_certs?.length}
										<div class="mt-0.5 flex flex-wrap gap-1">
											{#each sp.security_certs as cert}
												<span class="inline-flex rounded bg-blue-50 px-1.5 py-0.5 text-[10px] font-medium text-blue-600">{cert}</span>
											{/each}
										</div>
									{/if}
								</td>
								<td class="whitespace-nowrap px-4 py-3 text-sm text-gray-600">
									{getFlag(sp.country_code)} {sp.country}
								</td>
								<td class="px-4 py-3 text-sm text-gray-600 max-w-xs">{sp.purpose}</td>
								<td class="px-4 py-3 text-sm text-gray-500 max-w-xs">{formatCategories(sp.data_categories)}</td>
								<td class="whitespace-nowrap px-4 py-3 text-sm">
									{#if sp.cloud_act_risk}
										<span class="inline-flex items-center rounded-full bg-red-100 px-2 py-0.5 text-xs font-medium text-red-700">
											Risk
										</span>
									{:else}
										<span class="inline-flex items-center rounded-full bg-green-100 px-2 py-0.5 text-xs font-medium text-green-700">
											None
										</span>
									{/if}
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		</div>

		<!-- Data Flow Section -->
		<div class="rounded-lg border border-gray-200 bg-white">
			<div class="border-b border-gray-200 px-4 py-3">
				<h3 class="text-sm font-semibold text-gray-900">Data Flow</h3>
			</div>
			<div class="grid grid-cols-1 gap-4 p-4 sm:grid-cols-2">
				<div>
					<p class="text-xs font-medium uppercase tracking-wider text-gray-500">Storage Location</p>
					<p class="mt-1 text-sm text-gray-900">{report.data_flow.storage_location}</p>
				</div>
				<div>
					<p class="text-xs font-medium uppercase tracking-wider text-gray-500">Encryption</p>
					<div class="mt-1 flex gap-3">
						<span class="inline-flex items-center gap-1 text-sm text-gray-700">
							{#if report.data_flow.encryption_at_rest}
								<svg class="h-4 w-4 text-green-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M4.5 12.75l6 6 9-13.5" /></svg>
							{:else}
								<svg class="h-4 w-4 text-red-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12" /></svg>
							{/if}
							At rest
						</span>
						<span class="inline-flex items-center gap-1 text-sm text-gray-700">
							{#if report.data_flow.encryption_in_transit}
								<svg class="h-4 w-4 text-green-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M4.5 12.75l6 6 9-13.5" /></svg>
							{:else}
								<svg class="h-4 w-4 text-red-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12" /></svg>
							{/if}
							In transit
						</span>
					</div>
				</div>
				<div class="sm:col-span-2">
					<p class="text-xs font-medium uppercase tracking-wider text-gray-500">Cross-Border Transfers</p>
					{#if report.data_flow.cross_border_transfers}
						<p class="mt-1 text-sm text-amber-700">{report.data_flow.cross_border_details}</p>
					{:else}
						<p class="mt-1 text-sm text-green-700">No cross-border transfers. All data remains within the EU.</p>
					{/if}
				</div>
			</div>
		</div>

		<!-- Processing Activities -->
		<div class="rounded-lg border border-gray-200 bg-white">
			<div class="border-b border-gray-200 px-4 py-3">
				<h3 class="text-sm font-semibold text-gray-900">Processing Activities (Article 30)</h3>
			</div>
			<div class="overflow-x-auto">
				<table class="min-w-full divide-y divide-gray-200">
					<thead class="bg-gray-50">
						<tr>
							<th class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Activity</th>
							<th class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Legal Basis</th>
							<th class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Data Categories</th>
							<th class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Retention</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-gray-200">
						{#each report.processing_activities as activity}
							<tr class="hover:bg-gray-50">
								<td class="whitespace-nowrap px-4 py-3 text-sm font-medium text-gray-900">{activity.activity}</td>
								<td class="px-4 py-3 text-sm text-gray-600">{activity.legal_basis}</td>
								<td class="px-4 py-3 text-sm text-gray-500">{formatCategories(activity.data_categories)}</td>
								<td class="whitespace-nowrap px-4 py-3 text-sm text-gray-600">{activity.retention}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		</div>

		<!-- Entity Info -->
		<div class="rounded-lg border border-gray-200 bg-white p-4">
			<h3 class="text-sm font-semibold text-gray-900">Data Controller</h3>
			<p class="mt-2 text-sm text-gray-600">
				<span class="font-medium">{report.eurobase_entity.name}</span> &middot;
				{report.eurobase_entity.country} &middot;
				DPO: <a href="mailto:{report.eurobase_entity.dpo_email}" class="text-eurobase-600 hover:underline">{report.eurobase_entity.dpo_email}</a>
			</p>
		</div>
	{/if}
</div>
