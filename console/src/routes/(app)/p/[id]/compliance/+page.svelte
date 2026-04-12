<script lang="ts">
	import { page } from '$app/stores';
	import { api, type DPAReport, type AuditLogEntry } from '$lib/api.js';
	import { onMount } from 'svelte';

	let projectId = $derived($page.params.id);

	// Tab state
	let activeTab = $state<'dpa' | 'audit'>('dpa');

	// DPA Report
	let report: DPAReport | null = $state(null);
	let loading = $state(true);
	let error: string | null = $state(null);

	// Audit Log
	let auditEntries: AuditLogEntry[] = $state([]);
	let auditTotal = $state(0);
	let auditOffset = $state(0);
	let auditLoading = $state(false);
	let auditError: string | null = $state(null);
	let auditActionFilter = $state('');
	const auditPageSize = 50;

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

	async function loadAuditLog() {
		auditLoading = true;
		auditError = null;
		try {
			const result = await api.getAuditLog(projectId, {
				limit: auditPageSize,
				offset: auditOffset,
				action: auditActionFilter || undefined
			});
			auditEntries = result.entries;
			auditTotal = result.total;
		} catch (err) {
			auditError = err instanceof Error ? err.message : 'Failed to load audit log';
		} finally {
			auditLoading = false;
		}
	}

	function switchToAudit() {
		activeTab = 'audit';
		if (auditEntries.length === 0) loadAuditLog();
	}

	function auditPrev() {
		if (auditOffset > 0) { auditOffset = Math.max(0, auditOffset - auditPageSize); loadAuditLog(); }
	}
	function auditNext() {
		if (auditOffset + auditPageSize < auditTotal) { auditOffset += auditPageSize; loadAuditLog(); }
	}
	function applyAuditFilter() {
		auditOffset = 0;
		loadAuditLog();
	}

	function formatAction(action: string): string {
		return action.replace(/\./g, ' ').replace(/\b\w/g, c => c.toUpperCase());
	}

	function actionColor(action: string): string {
		if (action.includes('deleted')) return 'bg-red-100 text-red-700';
		if (action.includes('regenerated')) return 'bg-amber-100 text-amber-700';
		if (action.includes('created') || action.includes('set') || action.includes('invited')) return 'bg-green-100 text-green-700';
		return 'bg-blue-100 text-blue-700';
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
	<!-- Tab Switcher -->
	<div class="flex gap-1 rounded-lg bg-gray-100 p-1 w-fit">
		<button
			onclick={() => activeTab = 'dpa'}
			class="cursor-pointer rounded-md px-4 py-1.5 text-sm font-medium transition-colors {activeTab === 'dpa' ? 'bg-white text-gray-900 shadow-sm' : 'text-gray-500 hover:text-gray-700'}"
		>
			DPA Report
		</button>
		<button
			onclick={switchToAudit}
			class="cursor-pointer rounded-md px-4 py-1.5 text-sm font-medium transition-colors {activeTab === 'audit' ? 'bg-white text-gray-900 shadow-sm' : 'text-gray-500 hover:text-gray-700'}"
		>
			Audit Log
		</button>
	</div>

{#if activeTab === 'dpa'}
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

{:else}
	<!-- Audit Log Tab -->
	<div class="flex items-center justify-between">
		<h2 class="text-lg font-semibold text-gray-900">Audit Log</h2>
	</div>

	<!-- Filter -->
	<div class="flex items-center gap-2 rounded-lg border border-gray-200 bg-gray-50 p-3">
		<select
			bind:value={auditActionFilter}
			class="rounded-lg border border-gray-300 bg-white px-2 py-1.5 text-sm text-gray-700 focus:border-eurobase-500 focus:outline-none cursor-pointer"
		>
			<option value="">All actions</option>
			<option value="auth_config.updated">Auth Config Updated</option>
			<option value="api_keys.regenerated">API Keys Regenerated</option>
			<option value="project.created">Project Created</option>
			<option value="project.deleted">Project Deleted</option>
			<option value="vault.secret_set">Vault Secret Set</option>
			<option value="vault.secret_deleted">Vault Secret Deleted</option>
			<option value="oauth.secret_set">OAuth Secret Set</option>
			<option value="function.created">Function Created</option>
			<option value="function.deleted">Function Deleted</option>
			<option value="data.exported">Data Exported</option>
		</select>
		<button
			type="button"
			onclick={applyAuditFilter}
			class="cursor-pointer rounded-lg bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 transition-colors"
		>
			Apply
		</button>
	</div>

	{#if auditError}
		<div class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-600">{auditError}</div>
	{/if}

	<div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
		<div class="overflow-x-auto">
			<table class="w-full text-sm">
				<thead>
					<tr class="border-b border-gray-200 bg-gray-50">
						<th class="px-4 py-2.5 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Timestamp</th>
						<th class="px-4 py-2.5 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Actor</th>
						<th class="px-4 py-2.5 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Action</th>
						<th class="px-4 py-2.5 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Target</th>
						<th class="px-4 py-2.5 text-left text-xs font-medium uppercase tracking-wider text-gray-500">IP</th>
					</tr>
				</thead>
				<tbody>
					{#if auditLoading}
						{#each Array(5) as _}
							<tr class="border-b border-gray-100"><td class="px-4 py-3" colspan="5"><div class="h-4 animate-pulse rounded bg-gray-100 w-full"></div></td></tr>
						{/each}
					{:else if auditEntries.length === 0}
						<tr>
							<td class="px-4 py-8 text-center text-gray-400" colspan="5">
								No audit events recorded yet. Actions like auth config changes, API key regeneration, and project deletion are tracked here automatically.
							</td>
						</tr>
					{:else}
						{#each auditEntries as entry}
							<tr class="border-b border-gray-100 hover:bg-gray-50 transition-colors">
								<td class="px-4 py-2.5 text-xs text-gray-500 font-mono whitespace-nowrap">
									{formatDate(entry.created_at)}
								</td>
								<td class="px-4 py-2.5 text-xs text-gray-700">{entry.actor_email}</td>
								<td class="px-4 py-2.5">
									<span class="inline-flex rounded-full px-2 py-0.5 text-xs font-semibold {actionColor(entry.action)}">
										{formatAction(entry.action)}
									</span>
								</td>
								<td class="px-4 py-2.5 text-xs text-gray-500 font-mono">
									{#if entry.target_type}
										{entry.target_type}{entry.target_id ? `: ${entry.target_id.substring(0, 8)}...` : ''}
									{:else}
										—
									{/if}
								</td>
								<td class="px-4 py-2.5 text-xs text-gray-400 font-mono">{entry.ip_address ?? '—'}</td>
							</tr>
						{/each}
					{/if}
				</tbody>
			</table>
		</div>
	</div>

	<!-- Pagination -->
	<div class="flex items-center justify-between">
		<div class="text-sm text-gray-500">
			{#if auditTotal > 0}
				Showing {auditOffset + 1}–{Math.min(auditOffset + auditPageSize, auditTotal)} of {auditTotal}
			{:else}
				No entries
			{/if}
		</div>
		<div class="flex items-center gap-2">
			<button
				type="button"
				class="cursor-pointer rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
				disabled={auditOffset <= 0}
				onclick={auditPrev}
			>
				Previous
			</button>
			<button
				type="button"
				class="cursor-pointer rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
				disabled={auditOffset + auditPageSize >= auditTotal}
				onclick={auditNext}
			>
				Next
			</button>
		</div>
	</div>
{/if}
</div>
