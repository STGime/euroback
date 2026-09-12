<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { api, type UpgradeRecord } from '$lib/api.js';

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

	function stateBadgeClass(state: string): string {
		switch (state) {
			case 'confirmed':
			case 'live':
				return 'bg-emerald-600/20 text-emerald-300 border border-emerald-500/40';
			case 'failed':
				return 'bg-red-600/20 text-red-300 border border-red-500/40';
			case 'requested':
			case 'provisioning':
			case 'copying':
			case 'cutting_over':
				return 'bg-amber-600/20 text-amber-300 border border-amber-500/40';
			default:
				return 'bg-slate-600/20 text-slate-300 border border-slate-500/40';
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

<div class="p-6 space-y-6">
	<div>
		<h1 class="text-2xl font-bold text-white">Team-tier upgrades</h1>
		<p class="text-sm text-slate-400 mt-1">
			Free/Pro → Team tier migration state machine. Data-copy step is currently
			STUBBED (dedicated instance will have zero user data until the copy PR
			lands).
		</p>
	</div>

	<!-- Trigger form -->
	<div class="rounded-lg border border-slate-700 bg-slate-900 p-4 space-y-3">
		<h2 class="text-lg font-semibold text-white">Trigger upgrade</h2>
		<div class="flex flex-wrap gap-3 items-end">
			<div class="flex-1 min-w-[300px]">
				<label class="block text-xs text-slate-400 mb-1" for="project-id">
					Project ID (UUID)
				</label>
				<input
					id="project-id"
					type="text"
					bind:value={newProjectId}
					placeholder="00000000-0000-0000-0000-000000000000"
					class="w-full rounded bg-slate-800 border border-slate-700 px-3 py-2 text-sm text-white"
					disabled={triggerBusy}
				/>
			</div>
			<div>
				<label class="block text-xs text-slate-400 mb-1" for="plan">
					Target plan
				</label>
				<select
					id="plan"
					bind:value={newPlan}
					class="rounded bg-slate-800 border border-slate-700 px-3 py-2 text-sm text-white"
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
			<div class="text-sm text-red-300 bg-red-950/40 border border-red-800/40 rounded px-3 py-2">
				{triggerError}
			</div>
		{/if}
		{#if triggerSuccess}
			<div class="text-sm text-emerald-300 bg-emerald-950/40 border border-emerald-800/40 rounded px-3 py-2">
				{triggerSuccess}
			</div>
		{/if}
	</div>

	{#if actionError}
		<div class="text-sm text-red-300 bg-red-950/40 border border-red-800/40 rounded px-3 py-2">
			{actionError}
		</div>
	{/if}

	<!-- Recent upgrades -->
	<div class="rounded-lg border border-slate-700 bg-slate-900 overflow-hidden">
		<div class="flex items-center justify-between px-4 py-3 border-b border-slate-700">
			<h2 class="text-lg font-semibold text-white">Recent upgrades</h2>
			<span class="text-xs text-slate-400">
				{upgrades.length} row{upgrades.length === 1 ? '' : 's'}
				{#if hasNonTerminal()}· polling every 5s{/if}
			</span>
		</div>
		{#if loading}
			<div class="p-6 text-sm text-slate-400">Loading…</div>
		{:else if loadError}
			<div class="p-6 text-sm text-red-300">{loadError}</div>
		{:else if upgrades.length === 0}
			<div class="p-6 text-sm text-slate-500">No upgrades yet. Trigger one above.</div>
		{:else}
			<div class="overflow-x-auto">
				<table class="w-full text-sm">
					<thead class="bg-slate-800/50 text-slate-400 text-xs uppercase">
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
					<tbody class="divide-y divide-slate-800">
						{#each upgrades as u (u.id)}
							<tr class="hover:bg-slate-800/30">
								<td class="px-3 py-2 text-slate-300 whitespace-nowrap">
									{formatTs(u.started_at)}
								</td>
								<td class="px-3 py-2 text-slate-300 font-mono text-xs">
									{u.project_id.slice(0, 8)}…
								</td>
								<td class="px-3 py-2 text-slate-300">
									{u.from_plan} → {u.to_plan}
								</td>
								<td class="px-3 py-2">
									<span class="rounded px-2 py-0.5 text-xs font-medium {stateBadgeClass(u.state)}">
										{u.state}
									</span>
								</td>
								<td class="px-3 py-2 text-slate-400 text-xs">
									{formatTs(u.cutover_at)}
								</td>
								<td class="px-3 py-2 text-red-300 text-xs max-w-[300px] truncate" title={u.error ?? ''}>
									{u.error ?? '—'}
								</td>
								<td class="px-3 py-2 text-right whitespace-nowrap space-x-2">
									{#if u.state === 'live'}
										<button
											type="button"
											onclick={() => confirmUpgrade(u.id)}
											disabled={actionBusy !== null}
											class="rounded bg-emerald-700 hover:bg-emerald-600 disabled:opacity-50 px-3 py-1 text-xs text-white"
										>
											Confirm now
										</button>
									{/if}
									{#if u.state !== 'confirmed' && u.state !== 'failed'}
										<button
											type="button"
											onclick={() => abortUpgrade(u.id)}
											disabled={actionBusy !== null}
											class="rounded bg-red-700 hover:bg-red-600 disabled:opacity-50 px-3 py-1 text-xs text-white"
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
	</div>
</div>
