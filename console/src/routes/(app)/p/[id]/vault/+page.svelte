<script lang="ts">
	import { page } from '$app/stores';
	import { api, type VaultSecret } from '$lib/api.js';
	import { onMount } from 'svelte';

	let projectId = $derived($page.params.id);

	let secrets: VaultSecret[] = $state([]);
	let loading = $state(true);
	let error: string | null = $state(null);

	// Create form
	let showCreate = $state(false);
	let createName = $state('');
	let createValue = $state('');
	let createDesc = $state('');
	let createError: string | null = $state(null);
	let creating = $state(false);

	// Revealed secrets cache
	let revealedSecrets: Record<string, string> = $state({});
	let revealingName: string | null = $state(null);

	// Delete confirm
	let deleteConfirmName: string | null = $state(null);

	// Edit mode
	let editingName: string | null = $state(null);
	let editValue = $state('');
	let editDesc = $state('');
	let editError: string | null = $state(null);
	let saving = $state(false);

	onMount(() => { loadSecrets(); });

	async function loadSecrets() {
		loading = true;
		error = null;
		try {
			secrets = await api.listVaultSecrets(projectId);
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load secrets';
		} finally {
			loading = false;
		}
	}

	async function handleCreate() {
		if (!createName.trim() || !createValue.trim()) return;
		creating = true;
		createError = null;
		try {
			await api.setVaultSecret(projectId, {
				name: createName.trim(),
				value: createValue.trim(),
				description: createDesc.trim()
			});
			showCreate = false;
			createName = '';
			createValue = '';
			createDesc = '';
			await loadSecrets();
		} catch (err) {
			let msg = err instanceof Error ? err.message : 'Failed to create secret';
			const m = msg.match(/\{"error":"(.+?)"\}/);
			if (m) msg = m[1];
			createError = msg;
		} finally {
			creating = false;
		}
	}

	async function revealSecret(name: string) {
		if (revealedSecrets[name]) {
			// Toggle off
			const { [name]: _, ...rest } = revealedSecrets;
			revealedSecrets = rest;
			return;
		}
		revealingName = name;
		try {
			const secret = await api.getVaultSecret(projectId, name);
			revealedSecrets = { ...revealedSecrets, [name]: secret.value ?? '' };
		} catch {
			// ignore
		} finally {
			revealingName = null;
		}
	}

	function startEdit(sec: VaultSecret) {
		editingName = sec.name;
		editValue = '';
		editDesc = sec.description;
		editError = null;
	}

	async function handleSaveEdit() {
		if (!editingName) return;
		saving = true;
		editError = null;
		try {
			const data: { value?: string; description?: string } = {};
			if (editValue.trim()) data.value = editValue.trim();
			data.description = editDesc.trim();
			await api.updateVaultSecret(projectId, editingName, data);
			// Clear revealed cache for this secret if value changed
			if (editValue.trim()) {
				const { [editingName]: _, ...rest } = revealedSecrets;
				revealedSecrets = rest;
			}
			editingName = null;
			await loadSecrets();
		} catch (err) {
			let msg = err instanceof Error ? err.message : 'Failed to update secret';
			const m = msg.match(/\{"error":"(.+?)"\}/);
			if (m) msg = m[1];
			editError = msg;
		} finally {
			saving = false;
		}
	}

	async function handleDelete() {
		if (!deleteConfirmName) return;
		try {
			await api.deleteVaultSecret(projectId, deleteConfirmName);
			const { [deleteConfirmName]: _, ...rest } = revealedSecrets;
			revealedSecrets = rest;
			deleteConfirmName = null;
			await loadSecrets();
		} catch { /* ignore */ }
	}

	async function copySecret(name: string) {
		try {
			let value = revealedSecrets[name];
			if (!value) {
				const secret = await api.getVaultSecret(projectId, name);
				value = secret.value ?? '';
			}
			await navigator.clipboard.writeText(value);
		} catch { /* ignore */ }
	}
</script>

<div class="mx-auto max-w-4xl space-y-6">
	<!-- Header -->
	<div class="flex items-center justify-between">
		<div>
			<h2 class="text-lg font-semibold text-gray-900">Vault</h2>
			<p class="text-sm text-gray-500">Encrypted secrets storage. Values are AES-256-GCM encrypted at rest.</p>
		</div>
		<button
			type="button"
			class="cursor-pointer inline-flex items-center gap-1.5 rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 transition-colors"
			onclick={() => { showCreate = true; createError = null; }}
		>
			<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
			</svg>
			New Secret
		</button>
	</div>

	<!-- Error -->
	{#if error}
		<div class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">{error}</div>
	{/if}

	<!-- Secret list -->
	{#if loading}
		<div class="space-y-3">
			{#each Array(3) as _}
				<div class="h-20 animate-pulse rounded-xl border border-gray-200 bg-gray-50"></div>
			{/each}
		</div>
	{:else if secrets.length === 0}
		<div class="rounded-xl border border-gray-200 bg-white p-12 text-center">
			<svg class="mx-auto h-12 w-12 text-gray-300" fill="none" viewBox="0 0 24 24" stroke-width="1" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" d="M16.5 10.5V6.75a4.5 4.5 0 1 0-9 0v3.75m-.75 11.25h10.5a2.25 2.25 0 0 0 2.25-2.25v-6.75a2.25 2.25 0 0 0-2.25-2.25H6.75a2.25 2.25 0 0 0-2.25 2.25v6.75a2.25 2.25 0 0 0 2.25 2.25Z" />
			</svg>
			<h3 class="mt-3 text-sm font-semibold text-gray-700">No secrets stored</h3>
			<p class="mt-1 text-sm text-gray-400">Add encrypted secrets that your application can access via the API or SDK.</p>
		</div>
	{:else}
		<div class="space-y-3">
			{#each secrets as sec}
				<div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
					<div class="flex items-center gap-4 px-5 py-4">
						<!-- Lock icon -->
						<div class="shrink-0">
							<svg class="h-5 w-5 text-gray-400" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" d="M16.5 10.5V6.75a4.5 4.5 0 1 0-9 0v3.75m-.75 11.25h10.5a2.25 2.25 0 0 0 2.25-2.25v-6.75a2.25 2.25 0 0 0-2.25-2.25H6.75a2.25 2.25 0 0 0-2.25 2.25v6.75a2.25 2.25 0 0 0 2.25 2.25Z" />
							</svg>
						</div>

						<!-- Info -->
						<div class="flex-1 min-w-0">
							<div class="flex items-center gap-2">
								<code class="text-sm font-mono font-semibold text-gray-900">{sec.name}</code>
							</div>
							{#if sec.description}
								<p class="mt-0.5 text-xs text-gray-400">{sec.description}</p>
							{/if}
							<!-- Revealed value -->
							{#if revealedSecrets[sec.name] !== undefined}
								<div class="mt-2 flex items-center gap-2">
									<code class="rounded bg-gray-100 px-2 py-1 text-xs font-mono text-gray-700 break-all">{revealedSecrets[sec.name]}</code>
								</div>
							{:else}
								<div class="mt-1 text-xs text-gray-300 font-mono">{'*'.repeat(24)}</div>
							{/if}
						</div>

						<!-- Actions -->
						<div class="flex items-center gap-2 shrink-0">
							<button
								type="button"
								class="cursor-pointer rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors"
								onclick={() => revealSecret(sec.name)}
								disabled={revealingName === sec.name}
							>
								{#if revealingName === sec.name}
									...
								{:else if revealedSecrets[sec.name] !== undefined}
									Hide
								{:else}
									Reveal
								{/if}
							</button>
							<button
								type="button"
								class="cursor-pointer rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors"
								onclick={() => copySecret(sec.name)}
								title="Copy value to clipboard"
							>
								Copy
							</button>
							<button
								type="button"
								class="cursor-pointer rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors"
								onclick={() => startEdit(sec)}
							>
								Edit
							</button>
							<button
								type="button"
								class="cursor-pointer rounded p-1.5 text-gray-300 hover:bg-red-50 hover:text-red-500 transition-colors"
								onclick={() => (deleteConfirmName = sec.name)}
								title="Delete secret"
							>
								<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
									<path stroke-linecap="round" stroke-linejoin="round" d="m14.74 9-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 0 1-2.244 2.077H8.084a2.25 2.25 0 0 1-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 0 0-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 0 1 3.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 0 0-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 0 0-7.5 0" />
								</svg>
							</button>
						</div>
					</div>

					<!-- Timestamp -->
					<div class="border-t border-gray-100 bg-gray-50 px-5 py-2 flex items-center gap-4">
						<span class="text-[10px] text-gray-400">
							Created {new Date(sec.created_at).toLocaleString('en-GB', { month: 'short', day: 'numeric', year: 'numeric', hour: '2-digit', minute: '2-digit' })}
						</span>
						{#if sec.updated_at !== sec.created_at}
							<span class="text-[10px] text-gray-400">
								Updated {new Date(sec.updated_at).toLocaleString('en-GB', { month: 'short', day: 'numeric', year: 'numeric', hour: '2-digit', minute: '2-digit' })}
							</span>
						{/if}
					</div>
				</div>
			{/each}
		</div>
	{/if}

	<!-- Plan limit indicator -->
	{#if !loading && secrets.length > 0}
		<div class="text-xs text-gray-400 text-right">
			{secrets.length} secret{secrets.length !== 1 ? 's' : ''} stored
		</div>
	{/if}
</div>

<!-- Create Secret Modal -->
{#if showCreate}
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<button type="button" class="fixed inset-0 bg-black/50 cursor-default" onclick={() => (showCreate = false)} tabindex="-1" aria-label="Close"></button>
		<div class="relative z-10 w-full max-w-lg rounded-xl bg-white shadow-2xl">
			<div class="flex items-center justify-between border-b border-gray-200 px-6 py-4">
				<h2 class="text-lg font-semibold text-gray-900">New Secret</h2>
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
					<label for="secret-name" class="block text-sm font-medium text-gray-700 mb-1">Name</label>
					<input id="secret-name" type="text" bind:value={createName} placeholder="e.g. STRIPE_API_KEY"
						class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono text-gray-900 placeholder-gray-300 focus:border-eurobase-500 focus:outline-none" />
				</div>
				<div>
					<label for="secret-value" class="block text-sm font-medium text-gray-700 mb-1">Value</label>
					<input id="secret-value" type="password" bind:value={createValue} placeholder="Secret value"
						class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono text-gray-900 placeholder-gray-300 focus:border-eurobase-500 focus:outline-none" />
				</div>
				<div>
					<label for="secret-desc" class="block text-sm font-medium text-gray-700 mb-1">Description <span class="text-gray-400 font-normal">(optional)</span></label>
					<input id="secret-desc" type="text" bind:value={createDesc} placeholder="e.g. Production API key for Stripe"
						class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 placeholder-gray-300 focus:border-eurobase-500 focus:outline-none" />
				</div>
			</div>
			<div class="flex items-center justify-end gap-3 border-t border-gray-200 px-6 py-4">
				<button type="button" class="cursor-pointer rounded-lg border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors" onclick={() => (showCreate = false)}>Cancel</button>
				<button type="button" class="cursor-pointer rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 transition-colors disabled:opacity-50" disabled={!createName.trim() || !createValue.trim() || creating} onclick={handleCreate}>
					{creating ? 'Saving...' : 'Create Secret'}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Edit Secret Modal -->
{#if editingName}
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<button type="button" class="fixed inset-0 bg-black/50 cursor-default" onclick={() => (editingName = null)} tabindex="-1" aria-label="Close"></button>
		<div class="relative z-10 w-full max-w-lg rounded-xl bg-white shadow-2xl">
			<div class="flex items-center justify-between border-b border-gray-200 px-6 py-4">
				<h2 class="text-lg font-semibold text-gray-900">Edit Secret: <code class="font-mono">{editingName}</code></h2>
				<button type="button" class="cursor-pointer rounded-lg p-1.5 text-gray-400 hover:bg-gray-100 hover:text-gray-600" onclick={() => (editingName = null)} aria-label="Close">
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12" /></svg>
				</button>
			</div>
			<div class="px-6 py-5 space-y-4">
				{#if editError}
					<div class="flex items-start gap-2 rounded-lg border border-red-200 bg-red-50 px-4 py-3">
						<svg class="h-4 w-4 mt-0.5 shrink-0 text-red-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
						</svg>
						<p class="text-sm text-red-700">{editError}</p>
					</div>
				{/if}
				<div>
					<label for="edit-value" class="block text-sm font-medium text-gray-700 mb-1">New Value <span class="text-gray-400 font-normal">(leave empty to keep current)</span></label>
					<input id="edit-value" type="password" bind:value={editValue} placeholder="New secret value"
						class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono text-gray-900 placeholder-gray-300 focus:border-eurobase-500 focus:outline-none" />
				</div>
				<div>
					<label for="edit-desc" class="block text-sm font-medium text-gray-700 mb-1">Description</label>
					<input id="edit-desc" type="text" bind:value={editDesc}
						class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 placeholder-gray-300 focus:border-eurobase-500 focus:outline-none" />
				</div>
			</div>
			<div class="flex items-center justify-end gap-3 border-t border-gray-200 px-6 py-4">
				<button type="button" class="cursor-pointer rounded-lg border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors" onclick={() => (editingName = null)}>Cancel</button>
				<button type="button" class="cursor-pointer rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 transition-colors disabled:opacity-50" disabled={saving} onclick={handleSaveEdit}>
					{saving ? 'Saving...' : 'Save Changes'}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Delete Confirm -->
{#if deleteConfirmName}
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<button type="button" class="fixed inset-0 bg-black/50 cursor-default" onclick={() => (deleteConfirmName = null)} tabindex="-1" aria-label="Close"></button>
		<div class="relative z-10 w-full max-w-sm rounded-xl bg-white shadow-2xl p-6">
			<div class="flex items-center gap-3 mb-4">
				<div class="flex h-10 w-10 items-center justify-center rounded-full bg-red-100">
					<svg class="h-5 w-5 text-red-600" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z" />
					</svg>
				</div>
				<div>
					<h3 class="text-sm font-semibold text-gray-900">Delete Secret</h3>
					<p class="text-xs text-gray-500">Permanently delete <code class="font-mono font-semibold">{deleteConfirmName}</code>? This cannot be undone.</p>
				</div>
			</div>
			<div class="flex justify-end gap-3">
				<button type="button" class="cursor-pointer rounded-lg border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors" onclick={() => (deleteConfirmName = null)}>Cancel</button>
				<button type="button" class="cursor-pointer rounded-lg bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700 transition-colors" onclick={handleDelete}>Delete</button>
			</div>
		</div>
	</div>
{/if}
