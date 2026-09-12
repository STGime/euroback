<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { api, type UpgradeRecord, type UpgradeState } from '$lib/api.js';

	let upgrades = $state<UpgradeRecord[]>([]);
	let loading = $state(true);
	let loadError = $state<string | null>(null);

	// Trigger-upgrade form.
	let newProjectId = $state('');
	let newPlan = $state<'team' | 'legal_team'>('team');
	let triggerBusy = $state(false);
	let triggerError = $state<string | null>(null);
	let triggerSuccess = $state<string | null>(null);

	// Per-row action state (abort/confirm).
	let actionBusy = $state<string | null>(null);
	let actionError = $state<string | null>(null);

	// Poll every 5s while any upgrade is non-terminal, so state
	// transitions land in the UI without a manual refresh. Skip the
	// tick when actionBusy — avoids flashing stale data mid-mutation.
	let pollTimer: ReturnType<typeof setInterval> | null = null;

	async function refresh() {
		try {
			const r = await api.adminListUpgrades();
			upgrades = r.upgrades ?? [];
			loadError = null;
		} catch (err) {
			loadError = err instanceof Error ? err.message : String(err);
		} finally {
			loading = false;
		}
	}

	function hasNonTerminal() {
		return upgrades.some(
			(u) => u.state !== 'confirmed' && u.state !== 'failed'
		);
	}

	onMount(async () => {
		await refresh();
		pollTimer = setInterval(() => {
			if (actionBusy) return;
			if (!hasNonTerminal()) return; // no need to poll if nothing moving
			refresh();
		}, 5000);
	});

	onDestroy(() => {
		if (pollTimer) clearInterval(pollTimer);
	});

	async function triggerUpgrade() {
		if (!newProjectId.trim()) {
			triggerError = 'project ID required';
			return;
		}
		triggerBusy = true;
		triggerError = null;
		triggerSuccess = null;
		try {
			const r = await api.adminRequestUpgrade(newProjectId.trim(), newPlan);
			triggerSuccess = `Upgrade ${r.id} requested (state: ${r.state})`;
			newProjectId = '';
			await refresh();
		} catch (err) {
			triggerError = err instanceof Error ? err.message : String(err);
		} finally {
			triggerBusy = false;
		}
	}

	async function abortUpgrade(id: string) {
		if (!confirm(`Abort upgrade ${id}?`)) return;
		actionBusy = id;
		actionError = null;
		try {
			await api.adminAbortUpgrade(id);
			await refresh();
		} catch (err) {
			actionError = err instanceof Error ? err.message : String(err);
		} finally {
			actionBusy = null;
		}
	}

	async function confirmUpgrade(id: string) {
		if (
			!confirm(
				`Confirm upgrade ${id}? Source data on the shared cluster will be scheduled for cleanup (7-day rollback window skipped).`
			)
		)
			return;
		actionBusy = id;
		actionError = null;
		try {
			await api.adminConfirmUpgrade(id);
			await refresh();
		} catch (err) {
			actionError = err instanceof Error ? err.message : String(err);
		} finally {
			actionBusy = null;
		}
	}

	function stateBadgeClass(state: UpgradeState): string {
		switch (state) {
			case 'confirmed':
			case 'live':
				return 'bg-emerald-50 text-emerald-800 border border-emerald-200';
			case 'failed':
				return 'bg-red-50 text-red-800 border border-red-200';
			case 'requested':
			case 'provisioning':
			case 'copying':
			case 'cutting_over':
				return 'bg-amber-50 text-amber-800 border border-amber-200';
			default:
				return 'bg-gray-50 text-gray-800 border border-gray-200';
		}
	}

	function formatTs(ts: string | null | undefined): string {
		if (!ts) return '—';
		try {
			return new Date(ts).toLocaleString();
		} catch {
			return ts;
		}
	}
</script>

<div class="max-w-6xl mx-auto p-6 space-y-6">
	<header>
		<h1 class="text-2xl font-semibold text-gray-900">Team-tier upgrades</h1>
		<p class="text-sm text-gray-500 mt-1">
			Free/Pro → Team tier migration state machine. Data-copy step is currently
			STUBBED (dedicated instance will have zero user data until the copy PR
			lands).
		</p>
	</header>

	<!-- Trigger form -->
	<section class="rounded-md border border-gray-200 bg-white p-4 space-y-3">
		<h2 class="text-lg font-semibold text-gray-900">Trigger upgrade</h2>
		<div class="flex flex-wrap gap-3 items-end">
			<div class="flex-1 min-w-[300px]">
				<label class="block text-xs text-gray-500 mb-1" for="project-id">
					Project ID (UUID)
				</label>
				<input
					id="project-id"
					type="text"
					bind:value={newProjectId}
					placeholder="00000000-0000-0000-0000-000000000000"
					class="w-full rounded bg-white border border-gray-300 px-3 py-2 text-sm text-gray-900"
					disabled={triggerBusy}
				/>
			</div>
			<div>
				<label class="block text-xs text-gray-500 mb-1" for="plan">
					Target plan
				</label>
				<select
					id="plan"
					bind:value={newPlan}
					class="rounded bg-white border border-gray-300 px-3 py-2 text-sm text-gray-900"
					disabled={triggerBusy}
				>
					<option value="team">team</option>
					<option value="legal_team">legal_team</option>
				</select>
			</div>
			<button
				type="button"
				onclick={triggerUpgrade}
				disabled={triggerBusy || !newProjectId.trim()}
				class="rounded bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 px-4 py-2 text-sm text-white"
			>
				{triggerBusy ? 'Requesting…' : 'Request upgrade'}
			</button>
		</div>
		{#if triggerError}
			<div class="text-sm rounded-md bg-red-50 border border-red-200 px-3 py-2 text-red-800">
				{triggerError}
			</div>
		{/if}
		{#if triggerSuccess}
			<div class="text-sm rounded-md bg-emerald-50 border border-emerald-200 px-3 py-2 text-emerald-800">
				{triggerSuccess}
			</div>
		{/if}
	</section>

	{#if actionError}
		<div class="text-sm rounded-md bg-red-50 border border-red-200 px-3 py-2 text-red-800">
			{actionError}
		</div>
	{/if}

	<!-- Recent upgrades -->
	<section class="rounded-md border border-gray-200 bg-white overflow-hidden">
		<div class="flex items-center justify-between px-4 py-3 border-b border-gray-200">
			<h2 class="text-lg font-semibold text-gray-900">Recent upgrades</h2>
			<span class="text-xs text-gray-500" aria-live="polite">
				{upgrades.length} row{upgrades.length === 1 ? '' : 's'}
				{#if hasNonTerminal()}· polling every 5s{/if}
			</span>
		</div>
		{#if loading}
			<div class="p-6 text-sm text-gray-500">Loading…</div>
		{:else if loadError}
			<div class="p-6 text-sm text-red-700">{loadError}</div>
		{:else if upgrades.length === 0}
			<div class="p-6 text-sm text-gray-500">No upgrades yet. Trigger one above.</div>
		{:else}
			<div class="overflow-x-auto">
				<table class="w-full text-sm">
					<thead class="bg-gray-50 text-gray-500 text-xs uppercase">
						<tr>
							<th class="px-3 py-2 text-left font-medium">Started</th>
							<th class="px-3 py-2 text-left font-medium">Project</th>
							<th class="px-3 py-2 text-left font-medium">Plan</th>
							<th class="px-3 py-2 text-left font-medium">State</th>
							<th class="px-3 py-2 text-left font-medium">Cutover</th>
							<th class="px-3 py-2 text-left font-medium">Error</th>
							<th class="px-3 py-2 text-right font-medium">Actions</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-gray-100">
						{#each upgrades as u (u.id)}
							<tr class="hover:bg-gray-50">
								<td class="px-3 py-2 text-gray-700 whitespace-nowrap">
									{formatTs(u.started_at)}
								</td>
								<td class="px-3 py-2 text-gray-700 font-mono text-xs">
									{u.project_id.slice(0, 8)}…
								</td>
								<td class="px-3 py-2 text-gray-700">
									{u.from_plan} → {u.to_plan}
								</td>
								<td class="px-3 py-2">
									<span
										class="rounded px-2 py-0.5 text-xs font-medium {stateBadgeClass(u.state)}"
										role="status"
										aria-label="Upgrade state: {u.state}"
									>
										{u.state}
									</span>
								</td>
								<td class="px-3 py-2 text-gray-500 text-xs">
									{formatTs(u.cutover_at)}
								</td>
								<td
									class="px-3 py-2 text-red-700 text-xs max-w-[300px] truncate"
									title={u.error ?? ''}
									aria-label={u.error ? `Error: ${u.error}` : ''}
								>
									{u.error ?? '—'}
								</td>
								<td class="px-3 py-2 text-right whitespace-nowrap space-x-2">
									{#if u.state === 'live'}
										<button
											type="button"
											onclick={() => confirmUpgrade(u.id)}
											disabled={actionBusy !== null}
											class="rounded bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 px-3 py-1 text-xs text-white"
										>
											Confirm now
										</button>
									{/if}
									{#if u.state !== 'confirmed' && u.state !== 'failed'}
										<button
											type="button"
											onclick={() => abortUpgrade(u.id)}
											disabled={actionBusy !== null}
											class="rounded bg-red-600 hover:bg-red-500 disabled:opacity-50 px-3 py-1 text-xs text-white"
										>
											{actionBusy === u.id ? '…' : 'Abort'}
										</button>
									{/if}
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}
	</section>
</div>
