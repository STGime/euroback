<script lang="ts">
	import { page } from '$app/stores';
	import { onMount } from 'svelte';
	import { api, type Project } from '$lib/api.js';
	import RestoreConfirmModal from '../RestoreConfirmModal.svelte';
	import RestoreProgressPanel from '../RestoreProgressPanel.svelte';

	let projectId = $derived($page.params.id);
	let project = $state<Project | null>(null);
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
			project = await api.getProject(projectId);
		} catch (e: any) {
			error = e?.message ?? 'Failed to load project';
		}
		targetTimeInput = new Date(Date.now() - 3600 * 1000).toISOString().slice(0, 16);
	});

	let targetTime = $derived(targetTimeInput ? new Date(targetTimeInput) : null);
	let inWindow = $derived(
		targetTime !== null && targetTime > earliest && targetTime < latest
	);

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
		} catch (e: any) {
			confirmError = e?.message ?? 'Restore failed';
		} finally {
			confirmBusy = false;
		}
	}
</script>

<div class="space-y-6">
	<header>
		<h1 class="text-lg font-semibold text-gray-900">Point-in-time restore</h1>
		<p class="mt-1 text-xs text-gray-500">
			Restore your dedicated managed-PG instance to any second within the last {pitrDays} days.
			A new instance is provisioned; the old one is retained for 7 days.
		</p>
	</header>

	{#if error}
		<div class="rounded-md bg-red-50 border border-red-200 p-3 text-sm text-red-800">
			{error}
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
					disabled={!inWindow}
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
