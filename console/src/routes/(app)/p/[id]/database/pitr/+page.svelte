<script lang="ts">
	import { page } from '$app/stores';
	import { onMount } from 'svelte';
	import { api, type Project, type RestoreQuota } from '$lib/api.js';
	import RestoreConfirmModal from '../RestoreConfirmModal.svelte';
	import RestoreProgressPanel from '../RestoreProgressPanel.svelte';

	let projectId = $derived($page.params.id);
	let project = $state<Project | null>(null);
	let quota = $state<RestoreQuota | null>(null);
	let error = $state<string | null>(null);

	// PITR window — for now hardcoded to 7 days (matches
	// plan_limits.team.pitr_days). A future refinement can fetch
	// the actual value from /platform/config/plans.
	const pitrDays = 7;
	let earliest = $derived(new Date(Date.now() - pitrDays * 24 * 3600 * 1000));
	let latest = $derived(new Date(Date.now() - 60 * 1000)); // now-1min

	// Bound to the datetime-local input.
	let targetTimeInput = $state<string>('');
	// Preselect a sensible default: 1 hour ago.
	onMount(async () => {
		try {
			const [p, q] = await Promise.all([
				api.getProject(projectId),
				api.getRestoreQuota(projectId).catch(() => null),
			]);
			project = p;
			quota = q;
		} catch (e: any) {
			error = e?.message ?? 'Failed to load project';
		}
		targetTimeInput = new Date(Date.now() - 3600 * 1000).toISOString().slice(0, 16);
	});

	let targetTime = $derived(targetTimeInput ? new Date(targetTimeInput) : null);
	let inWindow = $derived(
		targetTime !== null && targetTime > earliest && targetTime < latest
	);

	// Restore button disabled when quota exhausted — server rejects
	// with 402 restore_quota_exceeded anyway, but the client check
	// keeps the UI honest before the round-trip. Same shape as the
	// Backups tab's `restoreDisabled`.
	let quotaExhausted = $derived(quota?.exhausted === true);

	let confirmOpen = $state(false);
	let confirmBusy = $state(false);
	let confirmError = $state<string | null>(null);
	let activeRestoreId = $state<string | null>(null);

	async function confirmRestore() {
		if (!targetTime) return;
		confirmBusy = true;
		confirmError = null;
		try {
			const r = await api.restoreFromPITR(projectId, targetTime.toISOString());
			activeRestoreId = r.restore_id;
			confirmOpen = false;
			// Refresh the badge so the increment shows immediately;
			// same shape as the Backups tab post-restore refresh.
			quota = await api.getRestoreQuota(projectId).catch(() => quota);
		} catch (e: any) {
			confirmError = e?.message ?? 'Restore failed';
		} finally {
			confirmBusy = false;
		}
	}
</script>

<div class="space-y-6">
	<header>
		<div class="flex items-start justify-between gap-4">
			<div>
				<h1 class="text-lg font-semibold text-gray-900">Point-in-time restore</h1>
				<p class="mt-1 text-xs text-gray-500">
					Restore your dedicated managed-PG instance to any second within the last {pitrDays} days.
					A new instance is provisioned; the old one is retained for 7 days.
					<span class="text-gray-600">Includes 1 restore per calendar month</span>
					— shared with snapshot-based restores from the
					<a href={`/p/${projectId}/database/backups`} class="text-eurobase-700 hover:underline">Backups</a> tab.
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

	{#if quotaExhausted}
		<div class="rounded-md bg-amber-50 border border-amber-200 p-3 text-sm text-amber-900">
			<strong>Restore quota reached.</strong>
			You've used {quota!.used} of {quota!.included} monthly restores.
			The counter resets on {new Date(quota!.resets_at).toLocaleDateString()}.
			If you need an additional restore before then, contact support.
		</div>
	{/if}

	{#if activeRestoreId}
		<RestoreProgressPanel
			{projectId}
			restoreId={activeRestoreId}
			onDismiss={() => (activeRestoreId = null)}
		/>
	{:else}
		<div class="rounded-lg border border-gray-200 bg-white p-6 shadow-sm space-y-4">
			<div>
				<label for="target-time" class="block text-sm font-medium text-gray-700 mb-1">
					Restore to point in time
				</label>
				<input
					id="target-time"
					type="datetime-local"
					bind:value={targetTimeInput}
					min={earliest.toISOString().slice(0, 16)}
					max={latest.toISOString().slice(0, 16)}
					class="rounded-md border border-gray-300 px-3 py-2 text-sm"
				/>
				<p class="mt-2 text-xs text-gray-500">
					Window: {earliest.toLocaleString()} → {latest.toLocaleString()} ({pitrDays} days).
				</p>
				{#if targetTime !== null && !inWindow}
					<p class="mt-1 text-xs text-red-600">Target must be inside the {pitrDays}-day PITR window.</p>
				{/if}
			</div>

			<div class="flex justify-end pt-2 border-t border-gray-100">
				<button
					type="button"
					onclick={() => {
						confirmError = null;
						confirmOpen = true;
					}}
					disabled={!inWindow || quotaExhausted}
					title={quotaExhausted ? 'Monthly restore quota reached — contact support' : undefined}
					class="cursor-pointer rounded-lg bg-red-600 px-4 py-2 text-sm font-semibold text-white hover:bg-red-700 disabled:cursor-not-allowed disabled:opacity-40"
				>
					Restore to this time
				</button>
			</div>
		</div>
	{/if}
</div>

<RestoreConfirmModal
	bind:open={confirmOpen}
	projectName={project?.name ?? ''}
	description={targetTime
		? `Restore project "${project?.name}" to the state as of ${targetTime.toLocaleString()}.`
		: 'Confirm restore.'}
	busy={confirmBusy}
	error={confirmError}
	onConfirm={confirmRestore}
/>
