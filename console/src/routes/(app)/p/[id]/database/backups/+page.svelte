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

	// Create-snapshot modal state.
	let createOpen = $state(false);
	let createBusy = $state(false);
	let createError = $state<string | null>(null);
	let createTag = $state('');
	const maxTagLen = 64;   // must match maxSnapshotTagLen in backup_handlers.go

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

	function openCreate() {
		createTag = '';
		createError = null;
		createOpen = true;
	}

	async function submitCreate() {
		createBusy = true;
		createError = null;
		try {
			const tag = createTag.trim();
			await api.createBackup(projectId, tag ? { tag } : {});
			createOpen = false;
			createTag = '';
			await refresh();
		} catch (e: any) {
			createError = e?.message ?? 'Failed to create snapshot';
		} finally {
			createBusy = false;
		}
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
					Automatic daily snapshots of your dedicated managed-PG instance
					(7-day retention on Team, 30-day on Legal Team). Take an
					<strong>on-demand snapshot</strong> before a risky migration and
					tag it so you can find it later; restore from it if the migration
					goes wrong.
					<span class="text-gray-600">Includes 1 restore per calendar month</span>
					— either scheduled or on-demand, both count against the same cap.
				</p>
			</div>
			<div class="flex shrink-0 items-start gap-2">
				<button
					type="button"
					onclick={openCreate}
					class="rounded-md bg-eurobase-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-eurobase-700 disabled:opacity-40"
				>
					Create snapshot
				</button>
				{#if quota}
					<div class="rounded-md border border-gray-200 bg-white px-3 py-2 text-right">
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
					<th class="px-4 py-2">Tag</th>
					<th class="px-4 py-2">Size</th>
					<th class="px-4 py-2">Created</th>
					<th class="px-4 py-2">Expires</th>
					<th class="px-4 py-2 text-right"></th>
				</tr>
			</thead>
			<tbody class="divide-y divide-gray-100">
				{#if loading}
					<tr><td colspan="7" class="px-4 py-6 text-center text-gray-400">Loading…</td></tr>
				{:else if backups.length === 0}
					<tr>
						<td colspan="7" class="px-4 py-6 text-center text-gray-400">
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
							<td class="px-4 py-2 text-gray-700">
								{#if b.tag}
									<span class="inline-block max-w-xs truncate rounded bg-gray-100 px-2 py-0.5 text-xs text-gray-700" title={b.tag}>{b.tag}</span>
								{:else}
									<span class="text-gray-300">—</span>
								{/if}
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

{#if createOpen}
	<!-- Create on-demand snapshot modal. Minimal — one optional tag
	     input. Rate-limited server-side to 5/day/project. -->
	<div class="fixed inset-0 z-40 flex items-center justify-center bg-black/40 p-4" role="dialog" aria-modal="true">
		<div class="w-full max-w-md rounded-lg bg-white p-5 shadow-xl">
			<h2 class="text-base font-semibold text-gray-900">Create on-demand snapshot</h2>
			<p class="mt-1 text-xs text-gray-500">
				Takes a snapshot of this project's dedicated Postgres instance right now.
				A tag helps you find this snapshot in the list later — e.g. <code class="text-gray-700">pre-migration-v2</code>, <code class="text-gray-700">before-cleanup</code>, <code class="text-gray-700">2026-Q1-close</code>.
				Rate-limited to 5 per day per project.
			</p>

			<label class="mt-4 block text-xs font-medium text-gray-700">
				Tag (optional)
				<input
					type="text"
					bind:value={createTag}
					maxlength={maxTagLen}
					placeholder="pre-migration-v2"
					class="mt-1 block w-full rounded-md border border-gray-300 px-2 py-1.5 text-sm focus:border-eurobase-600 focus:outline-none focus:ring-1 focus:ring-eurobase-600"
					disabled={createBusy}
				/>
				<span class="mt-1 block text-[10px] text-gray-400">{createTag.length}/{maxTagLen}</span>
			</label>

			{#if createError}
				<div class="mt-3 rounded-md bg-red-50 border border-red-200 p-2 text-xs text-red-800">
					{createError}
				</div>
			{/if}

			<div class="mt-5 flex justify-end gap-2">
				<button
					type="button"
					onclick={() => (createOpen = false)}
					disabled={createBusy}
					class="rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-40"
				>
					Cancel
				</button>
				<button
					type="button"
					onclick={submitCreate}
					disabled={createBusy}
					class="rounded-md bg-eurobase-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-eurobase-700 disabled:opacity-40"
				>
					{createBusy ? 'Creating…' : 'Create snapshot'}
				</button>
			</div>
		</div>
	</div>
{/if}
