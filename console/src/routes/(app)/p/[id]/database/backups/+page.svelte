<script lang="ts">
	import { page } from '$app/stores';
	import { onMount } from 'svelte';
	import { api, type BackupSnapshot, type Project, type RestoreQuota } from '$lib/api.js';
	import RestoreConfirmModal from '../RestoreConfirmModal.svelte';
	import RestoreProgressPanel from '../RestoreProgressPanel.svelte';

	let projectId = $derived($page.params.id);
	let project = $state<Project | null>(null);
	let backups = $state<BackupSnapshot[]>([]);
	let quota = $state<RestoreQuota | null>(null);
	let loading = $state(true);
	let error = $state<string | null>(null);

	let selectedSnapshotId = $state<string | null>(null);
	let confirmOpen = $state(false);
	let confirmBusy = $state(false);
	let confirmError = $state<string | null>(null);

	let activeRestoreId = $state<string | null>(null);

	async function refresh() {
		loading = true;
		error = null;
		try {
			// Quota fetched in parallel with the list — a stale/failing
			// quota shouldn't block the backup list from rendering, but
			// a fresh badge on load is nicer UX than "…" for two seconds.
			const [p, b, q] = await Promise.all([
				api.getProject(projectId),
				api.listBackups(projectId),
				api.getRestoreQuota(projectId).catch(() => null),
			]);
			project = p;
			backups = b.backups;
			quota = q;
		} catch (e: any) {
			error = e?.message ?? 'Failed to load backups';
		} finally {
			loading = false;
		}
	}

	onMount(refresh);

	function openRestoreConfirm(id: string) {
		selectedSnapshotId = id;
		confirmError = null;
		confirmOpen = true;
	}

	async function confirmRestore() {
		if (!selectedSnapshotId) return;
		confirmBusy = true;
		confirmError = null;
		try {
			const r = await api.restoreFromSnapshot(projectId, selectedSnapshotId);
			activeRestoreId = r.restore_id;
			confirmOpen = false;
			selectedSnapshotId = null;
			// Re-fetch the quota so the badge reflects the new usage
			// before the user sees the restore-progress panel.
			quota = await api.getRestoreQuota(projectId).catch(() => quota);
		} catch (e: any) {
			confirmError = e?.message ?? 'Restore failed';
		} finally {
			confirmBusy = false;
		}
	}

	function selectedSnapshot() {
		return backups.find((b) => b.id === selectedSnapshotId);
	}

	function humanSize(mb: number): string {
		if (mb >= 1024) return (mb / 1024).toFixed(1) + ' GB';
		return mb + ' MB';
	}

	function humanKind(k: string): string {
		return k === 'ondemand' ? 'On-demand' : 'Scheduled';
	}

	// Restore CTAs disabled when the monthly quota is exhausted.
	// `quota === null` (fetch failed) leaves the button enabled —
	// the server-side check in HandleCreateRestore is the source of
	// truth; the badge is display-only.
	let restoreDisabled = $derived(quota?.exhausted === true || activeRestoreId !== null);
</script>

<div class="space-y-6">
	<header>
		<div class="flex items-start justify-between gap-4">
			<div>
				<h1 class="text-lg font-semibold text-gray-900">Backups</h1>
				<p class="mt-1 text-xs text-gray-500">
					Automatic daily snapshots of your dedicated managed-PG instance.
					Retention follows your plan (Team: 7 days).
					For a specific point in time within the last 7 days, use
					<a href={`/p/${projectId}/database/pitr`} class="text-eurobase-700 hover:underline">
						Point-in-time restore</a>.
				</p>
			</div>
			{#if quota}
				<div class="shrink-0 rounded-md border border-gray-200 bg-white px-3 py-2 text-right">
					<div class="text-xs text-gray-500">Monthly restores</div>
					<div class="text-sm font-semibold {quota.exhausted ? 'text-red-700' : 'text-gray-900'}">
						{quota.used} of {quota.included} used
					</div>
					{#if quota.exhausted}
						<div class="mt-1 text-xs text-red-600">
							Resets {new Date(quota.resets_at).toLocaleDateString()}
						</div>
					{/if}
				</div>
			{/if}
		</div>
	</header>

	{#if error}
		<div class="rounded-md bg-red-50 border border-red-200 p-3 text-sm text-red-800">
			{error}
		</div>
	{/if}

	{#if quota?.exhausted}
		<div class="rounded-md bg-amber-50 border border-amber-200 p-3 text-sm text-amber-900">
			<strong>Restore quota reached.</strong>
			You've used {quota.used} of {quota.included} monthly restores.
			The counter resets on {new Date(quota.resets_at).toLocaleDateString()}.
			If you need an additional restore before then, contact support.
		</div>
	{/if}

	{#if activeRestoreId}
		<RestoreProgressPanel
			{projectId}
			restoreId={activeRestoreId}
			onDismiss={() => {
				activeRestoreId = null;
				refresh();
			}}
		/>
	{/if}

	<div class="rounded-md border border-gray-200 bg-white overflow-hidden">
		<table class="w-full text-sm">
			<thead class="bg-gray-50 text-left text-xs uppercase text-gray-500">
				<tr>
					<th class="px-4 py-2">Name</th>
					<th class="px-4 py-2">Kind</th>
					<th class="px-4 py-2">Size</th>
					<th class="px-4 py-2">Created</th>
					<th class="px-4 py-2">Expires</th>
					<th class="px-4 py-2 text-right"></th>
				</tr>
			</thead>
			<tbody class="divide-y divide-gray-100">
				{#if loading}
					<tr><td colspan="6" class="px-4 py-6 text-center text-gray-400">Loading…</td></tr>
				{:else if backups.length === 0}
					<tr>
						<td colspan="6" class="px-4 py-6 text-center text-gray-400">
							No backups yet. Scaleway automatic backups appear within 24 h.
						</td>
					</tr>
				{:else}
					{#each backups as b}
						<tr>
							<td class="px-4 py-2 font-mono text-xs text-gray-600">{b.name}</td>
							<td class="px-4 py-2">
								<span class="inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset {b.kind === 'ondemand' ? 'bg-eurobase-50 text-eurobase-700 ring-eurobase-600/20' : 'bg-gray-50 text-gray-600 ring-gray-500/10'}">
									{humanKind(b.kind)}
								</span>
							</td>
							<td class="px-4 py-2 text-gray-700">{humanSize(b.size_mb)}</td>
							<td class="px-4 py-2 text-gray-500">{new Date(b.created_at).toLocaleString()}</td>
							<td class="px-4 py-2 text-gray-500">{new Date(b.expires_at).toLocaleDateString()}</td>
							<td class="px-4 py-2 text-right">
								<button
									type="button"
									onclick={() => openRestoreConfirm(b.id)}
									disabled={restoreDisabled}
									title={quota?.exhausted ? 'Monthly restore quota reached — contact support' : undefined}
									class="text-xs text-red-600 hover:text-red-800 cursor-pointer disabled:cursor-not-allowed disabled:opacity-40"
								>
									Restore from this
								</button>
							</td>
						</tr>
					{/each}
				{/if}
			</tbody>
		</table>
	</div>
</div>

<RestoreConfirmModal
	bind:open={confirmOpen}
	projectName={project?.name ?? ''}
	description={selectedSnapshot()
		? `Restore project "${project?.name}" from snapshot "${selectedSnapshot()?.name}" (created ${new Date(selectedSnapshot()!.created_at).toLocaleString()}).`
		: 'Confirm restore.'}
	busy={confirmBusy}
	error={confirmError}
	onConfirm={confirmRestore}
/>
