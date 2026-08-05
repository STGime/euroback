<script lang="ts">
	// Shared progress panel for both Backups and PITR flows (Team-tier M3).
	// Polls the restore-operation state machine every 5s until the
	// terminal state (complete or failed).
	import { api, type RestoreOperation, type RestoreState } from '$lib/api.js';

	let { projectId, restoreId, onDismiss = () => {} }: {
		projectId: string;
		restoreId: string;
		onDismiss?: () => void;
	} = $props();

	let op = $state<RestoreOperation | null>(null);
	let error = $state<string | null>(null);
	let pollTimer: ReturnType<typeof setInterval> | null = null;

	async function poll() {
		try {
			op = await api.getRestoreOperation(projectId, restoreId);
			if (op.state === 'complete' || op.state === 'failed') {
				if (pollTimer) clearInterval(pollTimer);
				pollTimer = null;
			}
		} catch (e: any) {
			error = e?.message ?? 'Poll failed';
		}
	}

	$effect(() => {
		poll();
		pollTimer = setInterval(poll, 5000);
		return () => {
			if (pollTimer) clearInterval(pollTimer);
		};
	});

	const STAGES: { state: RestoreState; label: string }[] = [
		{ state: 'pending', label: 'Queued' },
		{ state: 'provisioning', label: 'Provisioning new instance' },
		{ state: 'verifying', label: 'Verifying' },
		{ state: 'cutover', label: 'Cutover' },
		{ state: 'complete', label: 'Complete' }
	];

	function stageIndex(s: RestoreState | undefined): number {
		if (!s) return -1;
		if (s === 'failed') return -1;
		return STAGES.findIndex((x) => x.state === s);
	}
</script>

<div class="rounded-lg border border-gray-200 bg-white p-6 shadow-sm">
	<div class="flex items-center justify-between">
		<h3 class="text-sm font-semibold text-gray-900">Restore in progress</h3>
		{#if op?.state === 'complete' || op?.state === 'failed'}
			<button
				type="button"
				onclick={onDismiss}
				class="text-xs text-gray-500 hover:text-gray-700 cursor-pointer"
			>
				Dismiss
			</button>
		{/if}
	</div>

	{#if error}
		<div class="mt-3 rounded-md bg-red-50 border border-red-200 p-3 text-sm text-red-800">
			{error}
		</div>
	{:else if !op}
		<p class="mt-3 text-sm text-gray-500">Loading…</p>
	{:else}
		<ol class="mt-4 space-y-2">
			{#each STAGES as stage, i}
				{@const currentIdx = stageIndex(op.state)}
				{@const isCurrent = op.state === stage.state}
				{@const isPast = currentIdx > i}
				<li class="flex items-center gap-3">
					<span
						class="flex h-6 w-6 items-center justify-center rounded-full text-xs font-semibold
						{isPast ? 'bg-emerald-500 text-white' : isCurrent ? 'bg-eurobase-600 text-white animate-pulse' : 'bg-gray-100 text-gray-400'}"
					>
						{#if isPast}✓{:else}{i + 1}{/if}
					</span>
					<span class="text-sm {isCurrent ? 'text-gray-900 font-medium' : isPast ? 'text-gray-700' : 'text-gray-400'}">
						{stage.label}
					</span>
				</li>
			{/each}
		</ol>

		{#if op.state === 'complete'}
			<div class="mt-4 rounded-md bg-emerald-50 border border-emerald-200 p-3 text-sm text-emerald-800">
				Restore complete. The old instance stays available for 7 days as a rollback safety net.
			</div>
		{:else if op.state === 'failed'}
			<div class="mt-4 rounded-md bg-red-50 border border-red-200 p-3 text-sm text-red-800">
				<p class="font-semibold">Restore failed</p>
				{#if op.error}<p class="mt-1 font-mono text-xs">{op.error}</p>{/if}
				<p class="mt-2">Your project stayed on the old instance — no data loss. You can try again with a different snapshot or PITR target.</p>
			</div>
		{:else}
			<p class="mt-4 text-xs text-gray-500">
				Cutover typically completes in 2–5 minutes. Your project stays served from the old instance until the swap.
			</p>
		{/if}
	{/if}
</div>
