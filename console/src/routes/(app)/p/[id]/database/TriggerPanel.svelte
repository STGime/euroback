<script lang="ts">
	import { api, type DBTrigger, type DBFunction } from '$lib/api.js';

	let {
		projectId,
		tableName,
		triggers = [],
		onChanged
	}: {
		projectId: string;
		tableName: string;
		triggers: DBTrigger[];
		onChanged: () => void;
	} = $props();

	// ---- panel state ----
	let expanded = $state(false);
	let error: string | null = $state(null);

	// ---- create-trigger form ----
	let creating = $state(false);
	let newName = $state('');
	let newFunction = $state('');
	let newTiming = $state<'BEFORE' | 'AFTER' | 'INSTEAD OF'>('BEFORE');
	let newLevel = $state<'ROW' | 'STATEMENT'>('ROW');
	let newWhen = $state('');
	let evtInsert = $state(false);
	let evtUpdate = $state(false);
	let evtDelete = $state(false);
	let evtTruncate = $state(false);

	// ---- function picker ----
	let triggerFns = $state<DBFunction[]>([]);
	let triggerFnsLoading = $state(false);
	let triggerFnsLoaded = $state(false);

	async function ensureTriggerFnsLoaded() {
		if (triggerFnsLoaded || triggerFnsLoading) return;
		triggerFnsLoading = true;
		try {
			triggerFns = await api.listTriggerFunctions(projectId);
			triggerFnsLoaded = true;
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load trigger functions';
		} finally {
			triggerFnsLoading = false;
		}
	}

	$effect(() => {
		if (expanded) ensureTriggerFnsLoaded();
	});

	function selectedEvents(): DBTrigger['events'] {
		const out: DBTrigger['events'] = [];
		if (evtInsert) out.push('INSERT');
		if (evtUpdate) out.push('UPDATE');
		if (evtDelete) out.push('DELETE');
		if (evtTruncate) out.push('TRUNCATE');
		return out;
	}

	function resetForm() {
		newName = '';
		newFunction = '';
		newTiming = 'BEFORE';
		newLevel = 'ROW';
		newWhen = '';
		evtInsert = evtUpdate = evtDelete = evtTruncate = false;
	}

	async function handleCreate() {
		const events = selectedEvents();
		if (!newName.trim() || !newFunction || events.length === 0) return;

		creating = true;
		error = null;
		try {
			await api.createTrigger(projectId, tableName, {
				name: newName.trim(),
				function_name: newFunction,
				timing: newTiming,
				events,
				level: newLevel,
				when_clause: newWhen.trim() || undefined
			});
			resetForm();
			onChanged();
		} catch (err) {
			const raw = err instanceof Error ? err.message : String(err);
			const jsonMatch = raw.match(/\{"error":"(.+?)"\}/);
			error = jsonMatch ? jsonMatch[1] : raw;
		} finally {
			creating = false;
		}
	}

	async function handleDrop(triggerName: string) {
		error = null;
		try {
			await api.dropTrigger(projectId, tableName, triggerName);
			onChanged();
		} catch (err) {
			const raw = err instanceof Error ? err.message : String(err);
			const jsonMatch = raw.match(/\{"error":"(.+?)"\}/);
			error = jsonMatch ? jsonMatch[1] : raw;
		}
	}

	function eventBadgeColor(evt: string): string {
		switch (evt) {
			case 'INSERT': return 'bg-green-100 text-green-700';
			case 'UPDATE': return 'bg-blue-100 text-blue-700';
			case 'DELETE': return 'bg-red-100 text-red-700';
			case 'TRUNCATE': return 'bg-amber-100 text-amber-700';
			default: return 'bg-gray-100 text-gray-700';
		}
	}
</script>

<div class="mt-3 rounded-lg border border-gray-200 bg-white overflow-hidden">
	<button
		type="button"
		class="cursor-pointer w-full flex items-center justify-between px-4 py-2.5 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors"
		onclick={() => (expanded = !expanded)}
	>
		<div class="flex items-center gap-2">
			<svg class="h-4 w-4 text-gray-400 transition-transform {expanded ? 'rotate-90' : ''}" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" d="m8.25 4.5 7.5 7.5-7.5 7.5" />
			</svg>
			Triggers
			<span class="text-xs text-gray-400">({triggers.length})</span>
		</div>
	</button>

	{#if expanded}
		<div class="border-t border-gray-200 px-4 py-3 space-y-3">
			{#if error}
				<div class="flex items-start gap-2 rounded-lg border border-red-200 bg-red-50 px-3 py-2">
					<svg class="h-4 w-4 mt-0.5 shrink-0 text-red-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
					</svg>
					<p class="text-sm text-red-700">{error}</p>
				</div>
			{/if}

			<!-- Trigger list -->
			{#if triggers.length === 0}
				<p class="text-xs text-gray-400">No triggers attached.</p>
			{:else}
				<div class="space-y-1">
					{#each triggers as tr}
						<div class="flex items-center justify-between rounded-lg bg-gray-50 px-3 py-2">
							<div class="flex flex-wrap items-center gap-1.5 min-w-0">
								<code class="text-xs font-mono text-gray-700 truncate">{tr.name}</code>
								<span class="rounded px-1 py-0.5 text-[9px] font-semibold bg-gray-200 text-gray-700">{tr.timing}</span>
								{#each tr.events as evt}
									<span class="rounded px-1 py-0.5 text-[9px] font-semibold {eventBadgeColor(evt)}">{evt}</span>
								{/each}
								<span class="rounded px-1 py-0.5 text-[9px] font-semibold bg-purple-100 text-purple-700">{tr.level}</span>
								<span class="text-xs text-gray-400">→ <code class="font-mono">{tr.function_name}()</code></span>
								{#if tr.when_clause}
									<span class="text-[10px] font-mono text-gray-500" title="WHEN clause">WHEN {tr.when_clause}</span>
								{/if}
							</div>
							<button
								type="button"
								class="cursor-pointer rounded p-1 text-gray-300 hover:bg-red-50 hover:text-red-500 transition-colors shrink-0"
								onclick={() => handleDrop(tr.name)}
								title="Drop trigger"
							>
								<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
									<path stroke-linecap="round" stroke-linejoin="round" d="m14.74 9-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 0 1-2.244 2.077H8.084a2.25 2.25 0 0 1-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 0 0-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 0 1 3.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 0 0-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 0 0-7.5 0" />
								</svg>
							</button>
						</div>
					{/each}
				</div>
			{/if}

			<!-- Create-trigger form -->
			<div class="rounded-lg border border-gray-200 bg-gray-50/50 px-3 py-3 space-y-2">
				<p class="text-xs font-semibold text-gray-700">+ New Trigger</p>

				{#if triggerFnsLoading}
					<p class="text-xs text-gray-400">Loading functions...</p>
				{:else if triggerFns.length === 0 && triggerFnsLoaded}
					<div class="rounded-lg bg-white border border-gray-200 px-3 py-2.5 text-xs text-gray-700 space-y-2">
						<p class="font-medium text-gray-800">No trigger functions in this schema yet.</p>
						<p>A trigger needs two things: a <strong>function</strong> (the code that runs) and an <strong>attachment</strong> (this panel, wiring it to a row event). You're missing the function. Create one first:</p>
						<ol class="ml-4 list-decimal space-y-1">
							<li>Open <a href="/p/{projectId}/cron" class="text-eurobase-600 hover:underline">Cron &amp; RPC</a> &rarr; <strong>Functions</strong> tab.</li>
							<li>Click <strong>+ New Function</strong>.</li>
							<li>Set <strong>Returns</strong> to <code class="bg-gray-100 rounded px-1">trigger (for DB triggers)</code>. Language stays PL/pgSQL.</li>
							<li>Write the body (typically references <code class="bg-gray-100 rounded px-1">NEW</code>, <code class="bg-gray-100 rounded px-1">OLD</code>, raises exceptions, returns <code class="bg-gray-100 rounded px-1">NEW</code> or <code class="bg-gray-100 rounded px-1">OLD</code>) and click <strong>Create</strong>.</li>
							<li>Come back here and pick it from the function dropdown above.</li>
						</ol>
						<p class="text-[11px] text-gray-500 italic">Trigger functions won't appear in the RPC list once created — they're hidden because they can't be called via <code class="bg-gray-100 rounded px-1">eb.db.rpc()</code>. The dropdown above will find them.</p>
					</div>
				{:else}
					<div class="grid grid-cols-1 sm:grid-cols-2 gap-2">
						<input
							type="text"
							bind:value={newName}
							placeholder="Trigger name"
							class="rounded-lg border border-gray-300 bg-white px-2 py-1.5 text-sm text-gray-700 focus:border-eurobase-500 focus:outline-none"
						/>
						<select
							bind:value={newFunction}
							class="rounded-lg border border-gray-300 bg-white px-2 py-1.5 text-sm text-gray-700 focus:border-eurobase-500 focus:outline-none cursor-pointer"
						>
							<option value="">Function...</option>
							{#each triggerFns as fn}
								<option value={fn.name}>{fn.name}()</option>
							{/each}
						</select>
						<select
							bind:value={newTiming}
							class="rounded-lg border border-gray-300 bg-white px-2 py-1.5 text-sm text-gray-700 focus:border-eurobase-500 focus:outline-none cursor-pointer"
						>
							<option value="BEFORE">BEFORE</option>
							<option value="AFTER">AFTER</option>
							<option value="INSTEAD OF">INSTEAD OF</option>
						</select>
						<select
							bind:value={newLevel}
							class="rounded-lg border border-gray-300 bg-white px-2 py-1.5 text-sm text-gray-700 focus:border-eurobase-500 focus:outline-none cursor-pointer"
						>
							<option value="ROW">FOR EACH ROW</option>
							<option value="STATEMENT">FOR EACH STATEMENT</option>
						</select>
					</div>

					<div class="flex flex-wrap gap-3 text-xs text-gray-700">
						<span class="text-xs font-medium text-gray-600 mr-1">Events:</span>
						<label class="flex items-center gap-1 cursor-pointer">
							<input type="checkbox" bind:checked={evtInsert} class="h-3.5 w-3.5 rounded border-gray-300 cursor-pointer" />
							INSERT
						</label>
						<label class="flex items-center gap-1 cursor-pointer">
							<input type="checkbox" bind:checked={evtUpdate} class="h-3.5 w-3.5 rounded border-gray-300 cursor-pointer" />
							UPDATE
						</label>
						<label class="flex items-center gap-1 cursor-pointer">
							<input type="checkbox" bind:checked={evtDelete} class="h-3.5 w-3.5 rounded border-gray-300 cursor-pointer" />
							DELETE
						</label>
						<label class="flex items-center gap-1 cursor-pointer">
							<input type="checkbox" bind:checked={evtTruncate} class="h-3.5 w-3.5 rounded border-gray-300 cursor-pointer" />
							TRUNCATE
						</label>
					</div>

					<input
						type="text"
						bind:value={newWhen}
						placeholder="WHEN clause (optional, e.g. OLD.email IS DISTINCT FROM NEW.email)"
						class="w-full rounded-lg border border-gray-300 bg-white px-2 py-1.5 text-xs font-mono text-gray-700 focus:border-eurobase-500 focus:outline-none"
					/>

					<div class="flex justify-end pt-1">
						<button
							type="button"
							class="cursor-pointer rounded-lg bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 transition-colors disabled:opacity-50"
							disabled={!newName.trim() || !newFunction || selectedEvents().length === 0 || creating}
							onclick={handleCreate}
						>
							{creating ? 'Creating...' : 'Create Trigger'}
						</button>
					</div>
				{/if}
			</div>
		</div>
	{/if}
</div>
