<script lang="ts">
	import { page } from '$app/stores';
	import { api, type CronJob } from '$lib/api.js';
	import { onMount } from 'svelte';

	let projectId = $derived($page.params.id);

	let jobs: CronJob[] = $state([]);
	let loading = $state(true);
	let error: string | null = $state(null);

	// Create / edit form
	let showForm = $state(false);
	let editingJob: CronJob | null = $state(null);
	let formName = $state('');
	let formSchedule = $state('');
	let formActionType: 'sql' | 'rpc' = $state('sql');
	let formAction = $state('');
	let formError: string | null = $state(null);
	let saving = $state(false);

	// Delete confirm
	let deleteConfirmId: string | null = $state(null);

	const schedulePresets = [
		{ label: 'Every minute', value: '* * * * *' },
		{ label: 'Every 5 minutes', value: '*/5 * * * *' },
		{ label: 'Every hour', value: '0 * * * *' },
		{ label: 'Every day at midnight', value: '0 0 * * *' },
		{ label: 'Every Monday 9am', value: '0 9 * * 1' },
		{ label: 'Custom', value: '' }
	];

	let selectedPreset = $state('');

	onMount(() => { loadJobs(); });

	async function loadJobs() {
		loading = true;
		error = null;
		try {
			jobs = await api.listCronJobs(projectId);
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load scheduled jobs';
		} finally {
			loading = false;
		}
	}

	function openCreate() {
		editingJob = null;
		formName = '';
		formSchedule = '';
		formActionType = 'sql';
		formAction = '';
		formError = null;
		selectedPreset = '';
		showForm = true;
	}

	function openEdit(job: CronJob) {
		editingJob = job;
		formName = job.name;
		formSchedule = job.schedule;
		formActionType = job.action_type as 'sql' | 'rpc';
		formAction = job.action;
		formError = null;
		// Check if schedule matches a preset
		const match = schedulePresets.find(p => p.value === job.schedule);
		selectedPreset = match ? match.value : '';
		showForm = true;
	}

	function handlePresetChange(value: string) {
		selectedPreset = value;
		if (value) {
			formSchedule = value;
		}
	}

	async function handleSave() {
		if (!formName.trim() || !formSchedule.trim() || !formAction.trim()) return;
		saving = true;
		formError = null;
		try {
			if (editingJob) {
				await api.updateCronJob(projectId, editingJob.id, {
					name: formName.trim(),
					schedule: formSchedule.trim(),
					action_type: formActionType,
					action: formAction.trim()
				});
			} else {
				await api.createCronJob(projectId, {
					name: formName.trim(),
					schedule: formSchedule.trim(),
					action_type: formActionType,
					action: formAction.trim()
				});
			}
			showForm = false;
			await loadJobs();
		} catch (err) {
			let msg = err instanceof Error ? err.message : 'Failed to save job';
			const m = msg.match(/\{"error":"(.+?)"\}/);
			if (m) msg = m[1];
			formError = msg;
		} finally {
			saving = false;
		}
	}

	async function toggleEnabled(job: CronJob) {
		try {
			await api.updateCronJob(projectId, job.id, { enabled: !job.enabled });
			await loadJobs();
		} catch { /* ignore */ }
	}

	async function handleDelete() {
		if (!deleteConfirmId) return;
		try {
			await api.deleteCronJob(projectId, deleteConfirmId);
			deleteConfirmId = null;
			await loadJobs();
		} catch { /* ignore */ }
	}

	function formatSchedule(schedule: string): string {
		const preset = schedulePresets.find(p => p.value === schedule);
		return preset && preset.value ? preset.label : schedule;
	}

	function formatLastRun(dateStr: string | null): string {
		if (!dateStr) return 'Never';
		return new Date(dateStr).toLocaleString('en-GB', {
			month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit'
		});
	}
</script>

<div class="mx-auto max-w-4xl space-y-6">
	<!-- Header -->
	<div class="flex items-center justify-between">
		<div>
			<h2 class="text-lg font-semibold text-gray-900">Scheduled Jobs</h2>
			<p class="text-sm text-gray-500">Run SQL statements or RPC functions on a cron schedule.</p>
		</div>
		<button
			type="button"
			class="cursor-pointer inline-flex items-center gap-1.5 rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 transition-colors"
			onclick={openCreate}
		>
			<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
			</svg>
			New Job
		</button>
	</div>

	<!-- Error -->
	{#if error}
		<div class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">{error}</div>
	{/if}

	<!-- Job list -->
	{#if loading}
		<div class="space-y-3">
			{#each Array(3) as _}
				<div class="h-24 animate-pulse rounded-xl border border-gray-200 bg-gray-50"></div>
			{/each}
		</div>
	{:else if jobs.length === 0}
		<div class="rounded-xl border border-gray-200 bg-white p-12 text-center">
			<svg class="mx-auto h-12 w-12 text-gray-300" fill="none" viewBox="0 0 24 24" stroke-width="1" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" d="M12 6v6h4.5m4.5 0a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" />
			</svg>
			<h3 class="mt-3 text-sm font-semibold text-gray-700">No scheduled jobs yet</h3>
			<p class="mt-1 text-sm text-gray-400">Create a cron job to run SQL or call functions on a schedule.</p>
			<button
				type="button"
				class="cursor-pointer mt-4 inline-flex items-center gap-1.5 rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 transition-colors"
				onclick={openCreate}
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
				</svg>
				Create Job
			</button>
		</div>
	{:else}
		<div class="space-y-3">
			{#each jobs as job}
				<div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
					<div class="flex items-center gap-4 px-5 py-4">
						<!-- Status dot -->
						<div class="shrink-0">
							<div class="h-2.5 w-2.5 rounded-full {job.enabled ? 'bg-green-500' : 'bg-gray-300'}"></div>
						</div>

						<!-- Info -->
						<div class="flex-1 min-w-0">
							<div class="flex items-center gap-2">
								<span class="font-medium text-sm text-gray-900">{job.name}</span>
								<span class="rounded bg-gray-100 px-1.5 py-0.5 text-[10px] font-medium uppercase text-gray-500">
									{job.action_type}
								</span>
							</div>
							<div class="mt-1 flex items-center gap-3 text-xs text-gray-400">
								<span class="font-mono">{job.schedule}</span>
								<span class="text-gray-300">|</span>
								<span>{formatSchedule(job.schedule)}</span>
							</div>
							<div class="mt-1.5 flex items-center gap-4 text-xs text-gray-400">
								<span>Last run: {formatLastRun(job.last_run_at)}</span>
								<span>Runs: {job.run_count}</span>
								{#if job.last_error}
									<span class="text-red-500" title={job.last_error}>Error</span>
								{/if}
							</div>
						</div>

						<!-- Actions -->
						<div class="flex items-center gap-2 shrink-0">
							<button
								type="button"
								class="cursor-pointer rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors"
								onclick={() => openEdit(job)}
							>
								Edit
							</button>
							<button
								type="button"
								class="cursor-pointer rounded-lg border px-3 py-1.5 text-xs font-medium transition-colors
									{job.enabled
										? 'border-gray-300 text-gray-600 hover:bg-gray-50'
										: 'border-green-300 text-green-700 hover:bg-green-50'}"
								onclick={() => toggleEnabled(job)}
							>
								{job.enabled ? 'Disable' : 'Enable'}
							</button>
							<button
								type="button"
								class="cursor-pointer rounded p-1.5 text-gray-300 hover:bg-red-50 hover:text-red-500 transition-colors"
								onclick={() => (deleteConfirmId = job.id)}
								title="Delete job"
							>
								<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
									<path stroke-linecap="round" stroke-linejoin="round" d="m14.74 9-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 0 1-2.244 2.077H8.084a2.25 2.25 0 0 1-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 0 0-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 0 1 3.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 0 0-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 0 0-7.5 0" />
								</svg>
							</button>
						</div>
					</div>

					<!-- Action preview -->
					<div class="border-t border-gray-100 bg-gray-50 px-5 py-3">
						<code class="text-xs font-mono text-gray-500 break-all">{job.action}</code>
					</div>
				</div>
			{/each}
		</div>
	{/if}
</div>

<!-- Create / Edit Modal -->
{#if showForm}
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<button type="button" class="fixed inset-0 bg-black/50 cursor-default" onclick={() => (showForm = false)} tabindex="-1" aria-label="Close"></button>
		<div class="relative z-10 w-full max-w-lg rounded-xl bg-white shadow-2xl">
			<div class="flex items-center justify-between border-b border-gray-200 px-6 py-4">
				<h2 class="text-lg font-semibold text-gray-900">{editingJob ? 'Edit Job' : 'New Scheduled Job'}</h2>
				<button type="button" class="cursor-pointer rounded-lg p-1.5 text-gray-400 hover:bg-gray-100 hover:text-gray-600" onclick={() => (showForm = false)} aria-label="Close">
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12" /></svg>
				</button>
			</div>
			<div class="px-6 py-5 space-y-4">
				{#if formError}
					<div class="flex items-start gap-2 rounded-lg border border-red-200 bg-red-50 px-4 py-3">
						<svg class="h-4 w-4 mt-0.5 shrink-0 text-red-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
						</svg>
						<p class="text-sm text-red-700">{formError}</p>
					</div>
				{/if}

				<!-- Name -->
				<div>
					<label for="cron-name" class="block text-sm font-medium text-gray-700 mb-1">Name</label>
					<input id="cron-name" type="text" bind:value={formName} placeholder="e.g. Cleanup old sessions"
						class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 placeholder-gray-300 focus:border-eurobase-500 focus:outline-none" />
				</div>

				<!-- Schedule -->
				<div>
					<label for="cron-schedule" class="block text-sm font-medium text-gray-700 mb-1">Schedule</label>
					<div class="flex flex-wrap gap-1.5 mb-2">
						{#each schedulePresets as preset}
							<button
								type="button"
								class="cursor-pointer rounded-full border px-3 py-1 text-xs font-medium transition-colors
									{selectedPreset === preset.value && preset.value !== ''
										? 'border-eurobase-300 bg-eurobase-50 text-eurobase-700'
										: 'border-gray-300 text-gray-500 hover:border-gray-400'}"
								onclick={() => handlePresetChange(preset.value)}
							>
								{preset.label}
							</button>
						{/each}
					</div>
					<input id="cron-schedule" type="text" bind:value={formSchedule} placeholder="*/5 * * * *"
						class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono text-gray-900 placeholder-gray-300 focus:border-eurobase-500 focus:outline-none" />
					<p class="mt-1 text-xs text-gray-400">Cron expression: minute hour day-of-month month day-of-week</p>
				</div>

				<!-- Action Type -->
				<div>
					<span class="block text-sm font-medium text-gray-700 mb-2">Action Type</span>
					<div class="flex gap-3">
						<label class="flex items-center gap-2 cursor-pointer">
							<input type="radio" bind:group={formActionType} value="sql"
								class="text-eurobase-600 focus:ring-eurobase-500" />
							<span class="text-sm text-gray-700">SQL Statement</span>
						</label>
						<label class="flex items-center gap-2 cursor-pointer">
							<input type="radio" bind:group={formActionType} value="rpc"
								class="text-eurobase-600 focus:ring-eurobase-500" />
							<span class="text-sm text-gray-700">RPC Function</span>
						</label>
					</div>
				</div>

				<!-- Action -->
				<div>
					<label for="cron-action" class="block text-sm font-medium text-gray-700 mb-1">
						{formActionType === 'sql' ? 'SQL Statement' : 'Function Name'}
					</label>
					<textarea
						id="cron-action"
						bind:value={formAction}
						placeholder={formActionType === 'sql' ? "DELETE FROM sessions WHERE expires_at < now()" : "cleanup_expired_sessions"}
						rows="3"
						class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono text-gray-900 placeholder-gray-300 focus:border-eurobase-500 focus:outline-none resize-y"
					></textarea>
					{#if formActionType === 'rpc'}
						<p class="mt-1 text-xs text-gray-400">The function will be called as SELECT function_name() in your project schema.</p>
					{/if}
				</div>
			</div>
			<div class="flex items-center justify-end gap-3 border-t border-gray-200 px-6 py-4">
				<button type="button" class="cursor-pointer rounded-lg border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors" onclick={() => (showForm = false)}>Cancel</button>
				<button
					type="button"
					class="cursor-pointer rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 transition-colors disabled:opacity-50"
					disabled={!formName.trim() || !formSchedule.trim() || !formAction.trim() || saving}
					onclick={handleSave}
				>
					{saving ? 'Saving...' : editingJob ? 'Save Changes' : 'Create Job'}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Delete Confirm -->
{#if deleteConfirmId}
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<button type="button" class="fixed inset-0 bg-black/50 cursor-default" onclick={() => (deleteConfirmId = null)} tabindex="-1" aria-label="Close"></button>
		<div class="relative z-10 w-full max-w-sm rounded-xl bg-white shadow-2xl p-6">
			<div class="flex items-center gap-3 mb-4">
				<div class="flex h-10 w-10 items-center justify-center rounded-full bg-red-100">
					<svg class="h-5 w-5 text-red-600" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z" />
					</svg>
				</div>
				<div>
					<h3 class="text-sm font-semibold text-gray-900">Delete Scheduled Job</h3>
					<p class="text-xs text-gray-500">This job will stop running and be permanently removed.</p>
				</div>
			</div>
			<div class="flex justify-end gap-3">
				<button type="button" class="cursor-pointer rounded-lg border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors" onclick={() => (deleteConfirmId = null)}>Cancel</button>
				<button type="button" class="cursor-pointer rounded-lg bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700 transition-colors" onclick={handleDelete}>Delete</button>
			</div>
		</div>
	</div>
{/if}
