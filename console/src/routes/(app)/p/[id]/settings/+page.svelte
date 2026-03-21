<script lang="ts">
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { getContext, onMount } from 'svelte';
	import { api, type Project, type APIKey } from '$lib/api.js';

	const projectCtx = getContext<{ id: string; project: Project | null }>('projectId');
	let projectId = $derived($page.params.id);

	// API Keys
	let keys: APIKey[] = $state([]);
	let keysLoading = $state(true);
	let showRegenConfirm = $state(false);
	let regenerating = $state(false);
	let newKeys: { public_key: string; secret_key: string } | null = $state(null);
	let copiedKey: string | null = $state(null);

	// Delete project
	let showDeleteConfirm = $state(false);
	let deleteConfirmName = $state('');
	let deleting = $state(false);
	let deleteError: string | null = $state(null);
	let deleteNameMatches = $derived(deleteConfirmName === projectCtx.project?.name);

	async function handleDeleteProject() {
		if (!deleteNameMatches) return;
		deleting = true;
		deleteError = null;
		try {
			await api.deleteProject(projectId);
			goto('/projects');
		} catch (err) {
			let msg = err instanceof Error ? err.message : 'Failed to delete project';
			const m = msg.match(/\{"error":"(.+?)"\}/);
			if (m) msg = m[1];
			deleteError = msg;
		} finally {
			deleting = false;
		}
	}

	onMount(async () => {
		try {
			keys = await api.listAPIKeys(projectId);
		} catch { /* ignore */ }
		keysLoading = false;
	});

	async function handleRegenerate() {
		regenerating = true;
		try {
			newKeys = await api.regenerateAPIKeys(projectId);
			showRegenConfirm = false;
			keys = await api.listAPIKeys(projectId);
		} catch { /* ignore */ }
		regenerating = false;
	}

	function copyKey(key: string) {
		navigator.clipboard.writeText(key);
		copiedKey = key;
		setTimeout(() => { if (copiedKey === key) copiedKey = null; }, 1500);
	}

	let publicKey = $derived(keys.find(k => k.type === 'public'));
	let secretKey = $derived(keys.find(k => k.type === 'secret'));
</script>

<div class="mx-auto max-w-4xl space-y-6">
	{#if projectCtx.project}
		<!-- Project Info -->
		<div class="rounded-xl border border-gray-200 bg-white p-6">
			<h2 class="text-lg font-semibold text-gray-900">Project Settings</h2>
			<div class="mt-4 grid grid-cols-1 gap-4 sm:grid-cols-2">
				<div>
					<label class="block text-sm font-medium text-gray-500">Project Name</label>
					<p class="mt-1 text-sm text-gray-900">{projectCtx.project.name}</p>
				</div>
				<div>
					<label class="block text-sm font-medium text-gray-500">Slug</label>
					<p class="mt-1 font-mono text-sm text-gray-900">{projectCtx.project.slug}</p>
				</div>
				<div>
					<label class="block text-sm font-medium text-gray-500">Region</label>
					<p class="mt-1 text-sm text-gray-900">{projectCtx.project.region}</p>
				</div>
				<div>
					<label class="block text-sm font-medium text-gray-500">Plan</label>
					<p class="mt-1 text-sm text-gray-900 capitalize">{projectCtx.project.plan}</p>
				</div>
			</div>
		</div>

		<!-- API Keys -->
		<div class="rounded-xl border border-gray-200 bg-white p-6">
			<div class="flex items-center justify-between">
				<div>
					<h2 class="text-lg font-semibold text-gray-900">API Keys</h2>
					<p class="mt-1 text-sm text-gray-500">Use these keys to authenticate API requests from your application.</p>
				</div>
				<button
					type="button"
					class="cursor-pointer rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors"
					onclick={() => (showRegenConfirm = true)}
				>
					Regenerate Keys
				</button>
			</div>

			<!-- Newly generated keys banner -->
			{#if newKeys}
				<div class="mt-4 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3">
					<div class="flex items-center justify-between mb-2">
						<p class="text-sm font-medium text-amber-800">New keys generated — copy them now, they won't be shown again</p>
						<button type="button" class="cursor-pointer text-amber-400 hover:text-amber-600" onclick={() => (newKeys = null)}>
							<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12" /></svg>
						</button>
					</div>
					<div class="space-y-2">
						<div class="flex items-center gap-2">
							<span class="text-xs font-medium text-amber-700 w-16">Public</span>
							<code class="flex-1 rounded bg-white border border-amber-200 px-2 py-1 text-xs font-mono text-gray-900">{newKeys.public_key}</code>
							<button type="button" class="cursor-pointer text-amber-500 hover:text-amber-700" onclick={() => copyKey(newKeys!.public_key)} title="Copy">
								{#if copiedKey === newKeys.public_key}
									<svg class="h-4 w-4 text-green-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
								{:else}
									<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M15.666 3.888A2.25 2.25 0 0 0 13.5 2.25h-3c-1.03 0-1.9.693-2.166 1.638m7.332 0c.055.194.084.4.084.612v0a.75.75 0 0 1-.75.75H9.75a.75.75 0 0 1-.75-.75v0c0-.212.03-.418.084-.612m7.332 0c.646.049 1.288.11 1.927.184 1.1.128 1.907 1.077 1.907 2.185V19.5a2.25 2.25 0 0 1-2.25 2.25H6.75A2.25 2.25 0 0 1 4.5 19.5V6.257c0-1.108.806-2.057 1.907-2.185a48.208 48.208 0 0 1 1.927-.184" /></svg>
								{/if}
							</button>
						</div>
						<div class="flex items-center gap-2">
							<span class="text-xs font-medium text-amber-700 w-16">Secret</span>
							<code class="flex-1 rounded bg-white border border-amber-200 px-2 py-1 text-xs font-mono text-gray-900">{newKeys.secret_key}</code>
							<button type="button" class="cursor-pointer text-amber-500 hover:text-amber-700" onclick={() => copyKey(newKeys!.secret_key)} title="Copy">
								{#if copiedKey === newKeys.secret_key}
									<svg class="h-4 w-4 text-green-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
								{:else}
									<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M15.666 3.888A2.25 2.25 0 0 0 13.5 2.25h-3c-1.03 0-1.9.693-2.166 1.638m7.332 0c.055.194.084.4.084.612v0a.75.75 0 0 1-.75.75H9.75a.75.75 0 0 1-.75-.75v0c0-.212.03-.418.084-.612m7.332 0c.646.049 1.288.11 1.927.184 1.1.128 1.907 1.077 1.907 2.185V19.5a2.25 2.25 0 0 1-2.25 2.25H6.75A2.25 2.25 0 0 1 4.5 19.5V6.257c0-1.108.806-2.057 1.907-2.185a48.208 48.208 0 0 1 1.927-.184" /></svg>
								{/if}
							</button>
						</div>
					</div>
				</div>
			{/if}

			<!-- Key list -->
			{#if keysLoading}
				<div class="mt-4 h-16 animate-pulse rounded-lg bg-gray-50"></div>
			{:else if keys.length === 0}
				<div class="mt-4 rounded-lg bg-gray-50 px-4 py-6 text-center">
					<p class="text-sm text-gray-400">No API keys found. Click "Regenerate Keys" to create a new pair.</p>
				</div>
			{:else}
				<div class="mt-4 space-y-3">
					{#if publicKey}
						<div class="flex items-center gap-3 rounded-lg border border-gray-200 px-4 py-3">
							<span class="inline-flex rounded-full bg-green-100 px-2 py-0.5 text-[10px] font-bold text-green-700">PUBLIC</span>
							<code class="flex-1 text-sm font-mono text-gray-700">{publicKey.key_prefix}••••••••••••</code>
							<span class="text-xs text-gray-400">
								Created {new Date(publicKey.created_at).toLocaleDateString('en-GB', { month: 'short', day: 'numeric', year: 'numeric' })}
							</span>
						</div>
					{/if}
					{#if secretKey}
						<div class="flex items-center gap-3 rounded-lg border border-gray-200 px-4 py-3">
							<span class="inline-flex rounded-full bg-red-100 px-2 py-0.5 text-[10px] font-bold text-red-700">SECRET</span>
							<code class="flex-1 text-sm font-mono text-gray-700">{secretKey.key_prefix}••••••••••••</code>
							<span class="text-xs text-gray-400">
								Created {new Date(secretKey.created_at).toLocaleDateString('en-GB', { month: 'short', day: 'numeric', year: 'numeric' })}
							</span>
						</div>
					{/if}
				</div>
				<p class="mt-3 text-xs text-gray-400">
					The public key is safe to use in client-side code. The secret key should only be used server-side.
				</p>
			{/if}
		</div>

		<!-- Danger Zone -->
		<div class="rounded-xl border border-red-200 bg-white p-6">
			<h2 class="text-lg font-semibold text-red-600">Danger Zone</h2>
			<p class="mt-1 text-sm text-gray-500">Irreversible actions for this project.</p>
			<div class="mt-4">
				<button
					type="button"
					class="cursor-pointer rounded-lg border border-red-300 px-4 py-2 text-sm font-medium text-red-600 hover:bg-red-50 transition-colors"
					onclick={() => { showDeleteConfirm = true; deleteConfirmName = ''; deleteError = null; }}
				>
					Delete Project
				</button>
			</div>
		</div>
	{/if}
</div>

<!-- Regenerate Confirm -->
{#if showRegenConfirm}
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<button type="button" class="fixed inset-0 bg-black/50 cursor-default" onclick={() => (showRegenConfirm = false)} tabindex="-1" aria-label="Close"></button>
		<div class="relative z-10 w-full max-w-sm rounded-xl bg-white shadow-2xl p-6">
			<div class="flex items-center gap-3 mb-4">
				<div class="flex h-10 w-10 items-center justify-center rounded-full bg-amber-100">
					<svg class="h-5 w-5 text-amber-600" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z" />
					</svg>
				</div>
				<div>
					<h3 class="text-sm font-semibold text-gray-900">Regenerate API Keys</h3>
					<p class="text-xs text-gray-500">This will invalidate all existing keys immediately.</p>
				</div>
			</div>
			<p class="text-sm text-gray-600 mb-5">Any applications using the current keys will stop working. Make sure to update your code with the new keys.</p>
			<div class="flex justify-end gap-3">
				<button type="button" class="cursor-pointer rounded-lg border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors" onclick={() => (showRegenConfirm = false)}>Cancel</button>
				<button type="button" class="cursor-pointer rounded-lg bg-amber-600 px-4 py-2 text-sm font-medium text-white hover:bg-amber-700 transition-colors disabled:opacity-50" disabled={regenerating} onclick={handleRegenerate}>
					{regenerating ? 'Regenerating...' : 'Regenerate'}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Delete Project Confirm -->
{#if showDeleteConfirm && projectCtx.project}
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<button type="button" class="fixed inset-0 bg-black/50 cursor-default" onclick={() => (showDeleteConfirm = false)} tabindex="-1" aria-label="Close"></button>
		<div class="relative z-10 w-full max-w-md rounded-xl bg-white shadow-2xl p-6">
			<div class="flex items-center gap-3 mb-4">
				<div class="flex h-10 w-10 items-center justify-center rounded-full bg-red-100">
					<svg class="h-5 w-5 text-red-600" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z" />
					</svg>
				</div>
				<div>
					<h3 class="text-sm font-semibold text-gray-900">Delete Project</h3>
					<p class="text-xs text-gray-500">This action is permanent and cannot be undone.</p>
				</div>
			</div>
			{#if deleteError}
				<div class="mb-4 flex items-start gap-2 rounded-lg border border-red-200 bg-red-50 px-3 py-2">
					<svg class="h-4 w-4 mt-0.5 shrink-0 text-red-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
					</svg>
					<p class="text-sm text-red-700">{deleteError}</p>
				</div>
			{/if}
			<p class="text-sm text-gray-600 mb-4">
				This will permanently delete <strong>{projectCtx.project.name}</strong>, including all database tables, storage files, API keys, and webhooks.
			</p>
			<div class="mb-5">
				<label for="confirm-name" class="block text-sm font-medium text-gray-700 mb-1">
					Type <strong>{projectCtx.project.name}</strong> to confirm
				</label>
				<input
					id="confirm-name"
					type="text"
					bind:value={deleteConfirmName}
					placeholder={projectCtx.project.name}
					class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 placeholder-gray-300 focus:border-red-500 focus:outline-none"
				/>
			</div>
			<div class="flex justify-end gap-3">
				<button type="button" class="cursor-pointer rounded-lg border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors" onclick={() => (showDeleteConfirm = false)}>Cancel</button>
				<button
					type="button"
					class="cursor-pointer rounded-lg bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
					disabled={!deleteNameMatches || deleting}
					onclick={handleDeleteProject}
				>
					{deleting ? 'Deleting...' : 'Delete Project'}
				</button>
			</div>
		</div>
	</div>
{/if}
