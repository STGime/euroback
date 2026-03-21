<script lang="ts">
	import { page } from '$app/stores';
	import { api, type Webhook, type WebhookDelivery } from '$lib/api.js';
	import { onMount } from 'svelte';

	let projectId = $derived($page.params.id);

	let webhooks: Webhook[] = $state([]);
	let loading = $state(true);
	let error: string | null = $state(null);

	// Create form
	let showCreate = $state(false);
	let createUrl = $state('');
	let createDesc = $state('');
	let createEvents: string[] = $state(['db.insert', 'db.update', 'db.delete']);
	let createError: string | null = $state(null);
	let creating = $state(false);

	// Deliveries
	let selectedWebhook: string | null = $state(null);
	let deliveries: WebhookDelivery[] = $state([]);
	let deliveriesLoading = $state(false);

	// Delete confirm
	let deleteConfirmId: string | null = $state(null);

	// Newly created secret (shown once)
	let newSecret: string | null = $state(null);

	const availableEvents = [
		'db.insert', 'db.update', 'db.delete',
		'storage.upload', 'storage.delete',
		'auth.user.created', 'auth.user.deleted'
	];

	onMount(() => { loadWebhooks(); });

	async function loadWebhooks() {
		loading = true;
		error = null;
		try {
			webhooks = await api.listWebhooks(projectId);
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load webhooks';
		} finally {
			loading = false;
		}
	}

	async function handleCreate() {
		if (!createUrl.trim()) return;
		creating = true;
		createError = null;
		try {
			const wh = await api.createWebhook(projectId, {
				url: createUrl.trim(),
				events: createEvents,
				description: createDesc.trim()
			});
			newSecret = wh.secret ?? null;
			showCreate = false;
			createUrl = '';
			createDesc = '';
			createEvents = ['db.insert', 'db.update', 'db.delete'];
			await loadWebhooks();
		} catch (err) {
			let msg = err instanceof Error ? err.message : 'Failed to create webhook';
			const m = msg.match(/\{"error":"(.+?)"\}/);
			if (m) msg = m[1];
			createError = msg;
		} finally {
			creating = false;
		}
	}

	async function toggleEnabled(wh: Webhook) {
		try {
			await api.updateWebhook(projectId, wh.id, { enabled: !wh.enabled });
			await loadWebhooks();
		} catch { /* ignore */ }
	}

	async function handleDelete() {
		if (!deleteConfirmId) return;
		try {
			await api.deleteWebhook(projectId, deleteConfirmId);
			deleteConfirmId = null;
			if (selectedWebhook === deleteConfirmId) selectedWebhook = null;
			await loadWebhooks();
		} catch { /* ignore */ }
	}

	async function viewDeliveries(webhookId: string) {
		if (selectedWebhook === webhookId) {
			selectedWebhook = null;
			return;
		}
		selectedWebhook = webhookId;
		deliveriesLoading = true;
		try {
			deliveries = await api.getWebhookDeliveries(projectId, webhookId);
		} catch {
			deliveries = [];
		} finally {
			deliveriesLoading = false;
		}
	}

	function toggleEvent(event: string) {
		if (createEvents.includes(event)) {
			createEvents = createEvents.filter(e => e !== event);
		} else {
			createEvents = [...createEvents, event];
		}
	}
</script>

<div class="mx-auto max-w-4xl space-y-6">
	<!-- Header -->
	<div class="flex items-center justify-between">
		<div>
			<h2 class="text-lg font-semibold text-gray-900">Webhooks</h2>
			<p class="text-sm text-gray-500">Receive HTTP callbacks when events occur in your project.</p>
		</div>
		<button
			type="button"
			class="cursor-pointer inline-flex items-center gap-1.5 rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 transition-colors"
			onclick={() => { showCreate = true; createError = null; }}
		>
			<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
			</svg>
			Add Webhook
		</button>
	</div>

	<!-- New secret banner -->
	{#if newSecret}
		<div class="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3">
			<div class="flex items-start gap-3">
				<svg class="h-5 w-5 mt-0.5 shrink-0 text-amber-500" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z" />
				</svg>
				<div class="flex-1">
					<p class="text-sm font-medium text-amber-800">Signing secret — copy it now, it won't be shown again</p>
					<code class="mt-1 block rounded bg-white px-3 py-1.5 text-xs font-mono text-gray-900 border border-amber-200">{newSecret}</code>
				</div>
				<button
					type="button"
					class="cursor-pointer shrink-0 rounded p-1 text-amber-400 hover:text-amber-600"
					onclick={() => { navigator.clipboard.writeText(newSecret!); }}
					title="Copy secret"
				>
					<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M15.666 3.888A2.25 2.25 0 0 0 13.5 2.25h-3c-1.03 0-1.9.693-2.166 1.638m7.332 0c.055.194.084.4.084.612v0a.75.75 0 0 1-.75.75H9.75a.75.75 0 0 1-.75-.75v0c0-.212.03-.418.084-.612m7.332 0c.646.049 1.288.11 1.927.184 1.1.128 1.907 1.077 1.907 2.185V19.5a2.25 2.25 0 0 1-2.25 2.25H6.75A2.25 2.25 0 0 1 4.5 19.5V6.257c0-1.108.806-2.057 1.907-2.185a48.208 48.208 0 0 1 1.927-.184" />
					</svg>
				</button>
				<button
					type="button"
					class="cursor-pointer shrink-0 rounded p-1 text-amber-400 hover:text-amber-600"
					onclick={() => (newSecret = null)}
				>
					<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12" />
					</svg>
				</button>
			</div>
		</div>
	{/if}

	<!-- Error -->
	{#if error}
		<div class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">{error}</div>
	{/if}

	<!-- Webhook list -->
	{#if loading}
		<div class="space-y-3">
			{#each Array(3) as _}
				<div class="h-20 animate-pulse rounded-xl border border-gray-200 bg-gray-50"></div>
			{/each}
		</div>
	{:else if webhooks.length === 0}
		<div class="rounded-xl border border-gray-200 bg-white p-12 text-center">
			<svg class="mx-auto h-12 w-12 text-gray-300" fill="none" viewBox="0 0 24 24" stroke-width="1" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" d="M7.5 21 3 16.5m0 0L7.5 12M3 16.5h13.5m0-13.5L21 7.5m0 0L16.5 12M21 7.5H7.5" />
			</svg>
			<h3 class="mt-3 text-sm font-semibold text-gray-700">No webhooks configured</h3>
			<p class="mt-1 text-sm text-gray-400">Add a webhook to receive event notifications via HTTP.</p>
		</div>
	{:else}
		<div class="space-y-3">
			{#each webhooks as wh}
				<div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
					<div class="flex items-center gap-4 px-5 py-4">
						<!-- Status dot -->
						<div class="shrink-0">
							<div class="h-2.5 w-2.5 rounded-full {wh.enabled ? 'bg-green-500' : 'bg-gray-300'}"></div>
						</div>

						<!-- Info -->
						<div class="flex-1 min-w-0">
							<div class="flex items-center gap-2">
								<code class="truncate text-sm font-mono text-gray-900">{wh.url}</code>
							</div>
							{#if wh.description}
								<p class="mt-0.5 text-xs text-gray-400">{wh.description}</p>
							{/if}
							<div class="mt-1.5 flex flex-wrap gap-1">
								{#each wh.events as event}
									<span class="rounded bg-gray-100 px-1.5 py-0.5 text-[10px] font-medium text-gray-600">{event}</span>
								{/each}
							</div>
						</div>

						<!-- Actions -->
						<div class="flex items-center gap-2 shrink-0">
							<button
								type="button"
								class="cursor-pointer rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors"
								onclick={() => viewDeliveries(wh.id)}
							>
								{selectedWebhook === wh.id ? 'Hide' : 'Deliveries'}
							</button>
							<button
								type="button"
								class="cursor-pointer rounded-lg border px-3 py-1.5 text-xs font-medium transition-colors
									{wh.enabled
										? 'border-gray-300 text-gray-600 hover:bg-gray-50'
										: 'border-green-300 text-green-700 hover:bg-green-50'}"
								onclick={() => toggleEnabled(wh)}
							>
								{wh.enabled ? 'Disable' : 'Enable'}
							</button>
							<button
								type="button"
								class="cursor-pointer rounded p-1.5 text-gray-300 hover:bg-red-50 hover:text-red-500 transition-colors"
								onclick={() => (deleteConfirmId = wh.id)}
								title="Delete webhook"
							>
								<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
									<path stroke-linecap="round" stroke-linejoin="round" d="m14.74 9-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 0 1-2.244 2.077H8.084a2.25 2.25 0 0 1-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 0 0-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 0 1 3.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 0 0-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 0 0-7.5 0" />
								</svg>
							</button>
						</div>
					</div>

					<!-- Deliveries panel -->
					{#if selectedWebhook === wh.id}
						<div class="border-t border-gray-100 bg-gray-50 px-5 py-4">
							<h4 class="text-xs font-semibold uppercase tracking-wider text-gray-500 mb-3">Recent Deliveries</h4>
							{#if deliveriesLoading}
								<p class="text-xs text-gray-400">Loading...</p>
							{:else if deliveries.length === 0}
								<p class="text-xs text-gray-400">No deliveries yet.</p>
							{:else}
								<div class="space-y-2 max-h-64 overflow-y-auto">
									{#each deliveries as d}
										<div class="flex items-center gap-3 rounded-lg bg-white px-3 py-2 border border-gray-200">
											<div class="h-2 w-2 rounded-full shrink-0 {d.success ? 'bg-green-500' : 'bg-red-500'}"></div>
											<span class="text-xs font-mono text-gray-600">{d.event}</span>
											<span class="text-xs text-gray-400">{d.status_code ?? '—'}</span>
											<span class="text-xs text-gray-400">{d.attempts} attempt{d.attempts !== 1 ? 's' : ''}</span>
											<span class="ml-auto text-[10px] text-gray-400">
												{new Date(d.created_at).toLocaleString('en-GB', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })}
											</span>
										</div>
									{/each}
								</div>
							{/if}
						</div>
					{/if}
				</div>
			{/each}
		</div>
	{/if}
</div>

<!-- Create Webhook Modal -->
{#if showCreate}
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<button type="button" class="fixed inset-0 bg-black/50 cursor-default" onclick={() => (showCreate = false)} tabindex="-1" aria-label="Close"></button>
		<div class="relative z-10 w-full max-w-lg rounded-xl bg-white shadow-2xl">
			<div class="flex items-center justify-between border-b border-gray-200 px-6 py-4">
				<h2 class="text-lg font-semibold text-gray-900">Add Webhook</h2>
				<button type="button" class="cursor-pointer rounded-lg p-1.5 text-gray-400 hover:bg-gray-100 hover:text-gray-600" onclick={() => (showCreate = false)} aria-label="Close">
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12" /></svg>
				</button>
			</div>
			<div class="px-6 py-5 space-y-4">
				{#if createError}
					<div class="flex items-start gap-2 rounded-lg border border-red-200 bg-red-50 px-4 py-3">
						<svg class="h-4 w-4 mt-0.5 shrink-0 text-red-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
						</svg>
						<p class="text-sm text-red-700">{createError}</p>
					</div>
				{/if}
				<div>
					<label for="wh-url" class="block text-sm font-medium text-gray-700 mb-1">Endpoint URL</label>
					<input id="wh-url" type="url" bind:value={createUrl} placeholder="https://example.com/webhooks/eurobase"
						class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono text-gray-900 placeholder-gray-300 focus:border-eurobase-500 focus:outline-none" />
				</div>
				<div>
					<label for="wh-desc" class="block text-sm font-medium text-gray-700 mb-1">Description <span class="text-gray-400 font-normal">(optional)</span></label>
					<input id="wh-desc" type="text" bind:value={createDesc} placeholder="e.g. Slack notification on new users"
						class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 placeholder-gray-300 focus:border-eurobase-500 focus:outline-none" />
				</div>
				<div>
					<span class="block text-sm font-medium text-gray-700 mb-2">Events</span>
					<div class="flex flex-wrap gap-2">
						{#each availableEvents as event}
							<button
								type="button"
								class="cursor-pointer rounded-full border px-3 py-1 text-xs font-medium transition-colors
									{createEvents.includes(event)
										? 'border-eurobase-300 bg-eurobase-50 text-eurobase-700'
										: 'border-gray-300 text-gray-500 hover:border-gray-400'}"
								onclick={() => toggleEvent(event)}
							>
								{event}
							</button>
						{/each}
					</div>
				</div>
			</div>
			<div class="flex items-center justify-end gap-3 border-t border-gray-200 px-6 py-4">
				<button type="button" class="cursor-pointer rounded-lg border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors" onclick={() => (showCreate = false)}>Cancel</button>
				<button type="button" class="cursor-pointer rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 transition-colors disabled:opacity-50" disabled={!createUrl.trim() || createEvents.length === 0 || creating} onclick={handleCreate}>
					{creating ? 'Creating...' : 'Create Webhook'}
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
					<h3 class="text-sm font-semibold text-gray-900">Delete Webhook</h3>
					<p class="text-xs text-gray-500">This will also delete all delivery history.</p>
				</div>
			</div>
			<div class="flex justify-end gap-3">
				<button type="button" class="cursor-pointer rounded-lg border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors" onclick={() => (deleteConfirmId = null)}>Cancel</button>
				<button type="button" class="cursor-pointer rounded-lg bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700 transition-colors" onclick={handleDelete}>Delete</button>
			</div>
		</div>
	</div>
{/if}
