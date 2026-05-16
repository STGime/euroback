<script lang="ts">
	import { page } from '$app/stores';
	import { api, type DPAReport, type AuditLogEntry, type EndUser } from '$lib/api.js';
	import { onMount } from 'svelte';

	let projectId = $derived($page.params.id);

	// Tab state
	let activeTab = $state<'dpa' | 'audit' | 'export'>('dpa');

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

	onMount(() => {
		loadReport();
		// Stop the export poll loop when the page unmounts.
		return () => {
			if (pollTimer !== null) {
				clearInterval(pollTimer);
				pollTimer = null;
			}
		};
	});

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
		DK: '\u{1F1E9}\u{1F1F0}',
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

	// Data Export tab state
	interface ExportEntry {
		id: string;
		status: string;
		format: string;
		user_id?: string;
		file_size?: number;
		download_url?: string;
		created_at: string;
		completed_at?: string;
		expires_at?: string;
	}

	let exports: ExportEntry[] = $state([]);
	let exportsLoading = $state(false);
	let exportError: string | null = $state(null);
	let exportFormat = $state<'json' | 'csv'>('json');
	let exportRequesting = $state(false);

	// Auto-poll: when any export row is still pending or running, re-fetch
	// the list every 3s until all rows have settled. Stops automatically
	// to avoid a wake-lock when nothing is in flight. Cleared on unmount
	// via the existing onMount cleanup (returned function).
	let pollTimer: ReturnType<typeof setInterval> | null = null;

	function startPollIfNeeded() {
		const anyInFlight = exports.some(e => e.status === 'pending' || e.status === 'running');
		if (anyInFlight && pollTimer === null) {
			pollTimer = setInterval(loadExports, 3000);
		} else if (!anyInFlight && pollTimer !== null) {
			clearInterval(pollTimer);
			pollTimer = null;
		}
	}

	async function loadExports() {
		exportsLoading = true;
		exportError = null;
		try {
			const data = await api.listExports(projectId);
			exports = (data.exports ?? []) as unknown as ExportEntry[];
			startPollIfNeeded();
		} catch (err) {
			exportError = err instanceof Error ? err.message : 'Failed to load exports';
		} finally {
			exportsLoading = false;
		}
	}

	async function requestTenantExport() {
		exportRequesting = true;
		exportError = null;
		try {
			await api.requestTenantExport(projectId, exportFormat);
			await loadExports();
		} catch (err) {
			const msg = err instanceof Error ? err.message : 'Failed to request export';
			// parseAPIError surfaces rate-limit text as a friendly message;
			// keep the explicit hint for the 429 case so the UI is unchanged.
			exportError = /rate.?limit/i.test(msg) ? 'Rate limit: 1 export per hour. Please wait.' : msg;
		} finally {
			exportRequesting = false;
		}
	}

	// ── Per-user DSAR export ────────────────────────────────────────
	//
	// The backend route (POST /platform/projects/{id}/compliance/user-export)
	// has been live since DSAR shipped; the page just never wired a
	// picker for the user_id. Add a debounced search-by-email box that
	// hits the existing list-users endpoint and lets the operator pick.
	let userSearch = $state('');
	let userSearchResults: EndUser[] = $state([]);
	let userSearching = $state(false);
	let selectedUser: EndUser | null = $state(null);
	let userExportFormat = $state<'json' | 'csv'>('json');
	let userExportRequesting = $state(false);
	let userExportError: string | null = $state(null);
	let userSearchTimer: ReturnType<typeof setTimeout> | null = null;

	function onUserSearchChange() {
		// Debounce: list-users is paginated and cheap, but spamming
		// keystrokes is wasteful. 250ms feels instant; tweak if needed.
		if (userSearchTimer !== null) clearTimeout(userSearchTimer);
		userSearchTimer = setTimeout(runUserSearch, 250);
	}

	async function runUserSearch() {
		const query = userSearch.trim();
		if (query === '') {
			userSearchResults = [];
			return;
		}
		userSearching = true;
		try {
			const result = await api.listEndUsers(projectId, { search: query, limit: 10 });
			userSearchResults = result.users;
		} catch (err) {
			userExportError = err instanceof Error ? err.message : 'Failed to search users';
		} finally {
			userSearching = false;
		}
	}

	function pickUser(u: EndUser) {
		selectedUser = u;
		userSearch = u.email ?? u.id;
		userSearchResults = [];
	}

	function clearSelection() {
		selectedUser = null;
		userSearch = '';
		userSearchResults = [];
	}

	async function requestUserExport() {
		if (!selectedUser) {
			userExportError = 'pick a user first';
			return;
		}
		userExportRequesting = true;
		userExportError = null;
		try {
			await api.requestUserExport(projectId, selectedUser.id, userExportFormat);
			clearSelection();
			await loadExports();
		} catch (err) {
			const msg = err instanceof Error ? err.message : 'Failed to request user export';
			userExportError = /rate.?limit/i.test(msg) ? 'Rate limit: 1 user export per 24 hours. Please wait.' : msg;
		} finally {
			userExportRequesting = false;
		}
	}

	async function refreshExportStatus(exportId: string) {
		try {
			const updated = await api.getExport(projectId, exportId);
			exports = exports.map(e => e.id === exportId ? (updated as unknown as ExportEntry) : e);
			startPollIfNeeded();
		} catch { /* ignore */ }
	}

	function formatBytes(bytes?: number): string {
		if (!bytes) return '-';
		if (bytes < 1024) return `${bytes} B`;
		if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
		return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
	}
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
		<button
			onclick={() => { activeTab = 'export'; if (exports.length === 0) loadExports(); }}
			class="cursor-pointer rounded-md px-4 py-1.5 text-sm font-medium transition-colors {activeTab === 'export' ? 'bg-white text-gray-900 shadow-sm' : 'text-gray-500 hover:text-gray-700'}"
		>
			Data Export
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
			<option value="schema.create_table">Table Created</option>
			<option value="schema.drop_table">Table Dropped</option>
			<option value="schema.add_column">Column Added</option>
			<option value="schema.drop_column">Column Dropped</option>
			<option value="schema.alter_column">Column Altered</option>
			<option value="schema.rename_table">Table Renamed</option>
			<option value="schema.toggle_rls">RLS Toggled</option>
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

{#if activeTab === 'export'}
	<div class="space-y-6">
		<div class="flex items-center justify-between">
			<div>
				<h2 class="text-lg font-semibold text-gray-900">Data Export (DSAR)</h2>
				<p class="mt-1 text-sm text-gray-500">
					Export all project data or a specific user's data for GDPR Article 15/20 compliance.
				</p>
			</div>
		</div>

		<!-- Request new export -->
		<div class="rounded-lg border border-gray-200 p-4 space-y-3">
			<h3 class="text-sm font-semibold text-gray-900">Request Full Project Export</h3>
			<p class="text-xs text-gray-500">Exports all tables, user records, storage manifest, and audit log as a zip file.</p>
			<div class="flex items-center gap-3">
				<select bind:value={exportFormat} class="rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none">
					<option value="json">JSON</option>
					<option value="csv">CSV</option>
				</select>
				<button
					onclick={requestTenantExport}
					disabled={exportRequesting}
					class="cursor-pointer inline-flex items-center gap-2 rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
				>
					{#if exportRequesting}
						<svg class="h-4 w-4 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path></svg>
						Requesting...
					{:else}
						Export Project Data
					{/if}
				</button>
			</div>
			{#if exportError}
				<p class="text-xs text-red-600">{exportError}</p>
			{/if}
			<div class="rounded-lg bg-blue-50 border border-blue-200 p-3">
				<p class="text-xs text-blue-700">Exports are processed in the background. Large projects may take a few minutes. Download links expire after 7 days. All data remains in EU infrastructure (Scaleway fr-par).</p>
			</div>
		</div>

		<!-- Request single-user export -->
		<div class="rounded-lg border border-gray-200 p-4 space-y-3">
			<h3 class="text-sm font-semibold text-gray-900">Request Single-User Export</h3>
			<p class="text-xs text-gray-500">
				When an end-user files a GDPR Article 15 Subject Access Request,
				use this to export only their data — rows from every table that
				references their <code class="rounded bg-gray-100 px-1 py-0.5 text-[11px] font-mono text-gray-700">user_id</code>,
				plus their auth record. Rate-limited to 1 export per user per 24 hours.
			</p>

			<!-- Search box -->
			<div class="relative">
				<input
					type="text"
					bind:value={userSearch}
					oninput={onUserSearchChange}
					placeholder="Search by email or paste a user UUID"
					class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none"
				/>
				{#if userSearchResults.length > 0 && !selectedUser}
					<ul class="absolute z-10 mt-1 w-full overflow-hidden rounded-lg border border-gray-200 bg-white shadow-lg max-h-60 overflow-y-auto">
						{#each userSearchResults as u}
							<li>
								<button
									type="button"
									onclick={() => pickUser(u)}
									class="cursor-pointer w-full px-3 py-2 text-left text-sm text-gray-700 hover:bg-eurobase-50 hover:text-eurobase-700 flex items-center justify-between gap-2"
								>
									<span class="truncate">
										<span class="font-medium">{u.email ?? u.phone ?? 'no email'}</span>
										{#if u.display_name}<span class="text-gray-400"> · {u.display_name}</span>{/if}
									</span>
									<span class="shrink-0 font-mono text-[10px] text-gray-400">{u.id.substring(0, 8)}…</span>
								</button>
							</li>
						{/each}
					</ul>
				{:else if userSearch.trim() !== '' && !selectedUser && !userSearching}
					<p class="mt-1 text-xs text-gray-400">No users matched.</p>
				{/if}
			</div>

			{#if selectedUser}
				<div class="rounded-lg border border-eurobase-200 bg-eurobase-50 px-3 py-2 flex items-center justify-between gap-2">
					<div class="text-sm">
						<span class="font-medium text-gray-900">{selectedUser.email ?? selectedUser.phone ?? '(no email)'}</span>
						<span class="font-mono text-[11px] text-gray-500 ml-2">{selectedUser.id}</span>
					</div>
					<button
						onclick={clearSelection}
						class="cursor-pointer text-xs text-gray-500 hover:text-gray-700 font-medium"
					>
						Clear
					</button>
				</div>
			{/if}

			<div class="flex items-center gap-3">
				<select
					bind:value={userExportFormat}
					class="rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none"
				>
					<option value="json">JSON</option>
					<option value="csv">CSV</option>
				</select>
				<button
					onclick={requestUserExport}
					disabled={userExportRequesting || !selectedUser}
					class="cursor-pointer inline-flex items-center gap-2 rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
				>
					{#if userExportRequesting}
						<svg class="h-4 w-4 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path></svg>
						Requesting...
					{:else}
						Export User Data
					{/if}
				</button>
			</div>
			{#if userExportError}
				<p class="text-xs text-red-600">{userExportError}</p>
			{/if}
		</div>

		<!-- Export history -->
		<div class="rounded-lg border border-gray-200 overflow-hidden">
			<div class="px-4 py-3 bg-gray-50 border-b border-gray-200 flex items-center justify-between">
				<h3 class="text-sm font-semibold text-gray-900">Export History</h3>
				<button onclick={loadExports} class="cursor-pointer text-xs text-eurobase-600 hover:text-eurobase-700 font-medium">Refresh</button>
			</div>
			{#if exportsLoading}
				<div class="px-4 py-6 text-center text-sm text-gray-500">Loading...</div>
			{:else if exports.length === 0}
				<div class="px-4 py-6 text-center text-sm text-gray-500">No exports yet.</div>
			{:else}
				<table class="min-w-full divide-y divide-gray-200">
					<thead class="bg-gray-50">
						<tr>
							<th class="px-4 py-2 text-left text-xs font-medium text-gray-500 uppercase">Status</th>
							<th class="px-4 py-2 text-left text-xs font-medium text-gray-500 uppercase">Type</th>
							<th class="px-4 py-2 text-left text-xs font-medium text-gray-500 uppercase">Format</th>
							<th class="px-4 py-2 text-left text-xs font-medium text-gray-500 uppercase">Size</th>
							<th class="px-4 py-2 text-left text-xs font-medium text-gray-500 uppercase">Requested</th>
							<th class="px-4 py-2 text-left text-xs font-medium text-gray-500 uppercase">Action</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-gray-200 bg-white">
						{#each exports as exp}
							<tr>
								<td class="px-4 py-2 text-xs">
									<span class="inline-flex items-center rounded-full px-2 py-0.5 text-[10px] font-medium
										{exp.status === 'completed' ? 'bg-green-100 text-green-700' :
										 exp.status === 'failed' ? 'bg-red-100 text-red-700' :
										 exp.status === 'running' ? 'bg-blue-100 text-blue-700' :
										 'bg-gray-100 text-gray-700'}">
										{exp.status}
									</span>
								</td>
								<td class="px-4 py-2 text-xs text-gray-700">
									{#if exp.user_id}
										<span>User</span>
										<span class="ml-1 font-mono text-[10px] text-gray-400">{exp.user_id.substring(0, 8)}…</span>
									{:else}
										Project
									{/if}
								</td>
								<td class="px-4 py-2 text-xs text-gray-700 uppercase">{exp.format}</td>
								<td class="px-4 py-2 text-xs text-gray-700">{formatBytes(exp.file_size)}</td>
								<td class="px-4 py-2 text-xs text-gray-500">{formatDate(exp.created_at)}</td>
								<td class="px-4 py-2 text-xs">
									{#if exp.status === 'completed' && exp.download_url}
										<a href={exp.download_url} target="_blank" rel="noopener" class="text-eurobase-600 hover:text-eurobase-700 font-medium">Download</a>
									{:else if exp.status === 'pending' || exp.status === 'running'}
										<button onclick={() => refreshExportStatus(exp.id)} class="cursor-pointer text-eurobase-600 hover:text-eurobase-700 font-medium">Refresh</button>
									{:else if exp.status === 'failed'}
										<span class="text-red-500">Failed</span>
									{/if}
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			{/if}
		</div>
	</div>
{/if}
</div>
