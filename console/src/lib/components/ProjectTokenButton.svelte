<script lang="ts">
	// Creates a project-scoped personal access token (#702) right where it's
	// needed (Connect page MCP setups), so users don't have to detour to the
	// Account page. The token is shown once.
	import { api } from '$lib/api';

	let {
		projectId,
		defaultRole = 'viewer',
		tokenName = 'mcp'
	}: { projectId: string; defaultRole?: 'viewer' | 'developer' | 'admin'; tokenName?: string } = $props();

	// The default only picks the starting choice; the user changes it here.
	// svelte-ignore state_referenced_locally
	let role = $state<'viewer' | 'developer' | 'admin'>(defaultRole);
	let creating = $state(false);
	let token = $state('');
	let error = $state('');
	let copied = $state(false);

	async function create() {
		creating = true;
		error = '';
		try {
			const res = await api.createPAT(tokenName, projectId, role);
			token = res.token;
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to create the token';
		} finally {
			creating = false;
		}
	}

	async function copy() {
		try {
			await navigator.clipboard.writeText(token);
			copied = true;
			setTimeout(() => (copied = false), 2000);
		} catch {
			// clipboard unavailable
		}
	}
</script>

<div class="mt-3 rounded-lg border border-gray-200 bg-white p-3 text-xs">
	{#if token}
		<p class="font-medium text-gray-900">Your token — copy it now, it's shown only once</p>
		<div class="mt-2 flex items-center gap-2">
			<code class="flex-1 overflow-x-auto whitespace-nowrap rounded bg-gray-900 px-2 py-1.5 font-mono text-gray-100">{token}</code>
			<button type="button" onclick={copy} class="rounded border border-gray-300 px-2 py-1 font-medium text-gray-700 hover:bg-gray-50 cursor-pointer">{copied ? 'Copied!' : 'Copy'}</button>
		</div>
		<p class="mt-2 text-gray-500">Paste it as the Bearer token. It works only for this project; revoke it in <a href="/account" class="text-eurobase-700 hover:underline">Account → Tokens</a>.</p>
	{:else}
		<div class="flex flex-wrap items-center gap-2">
			<span class="text-gray-700">Create a token for this project:</span>
			<select bind:value={role} class="rounded border border-gray-300 px-2 py-1 text-xs text-gray-900">
				<option value="viewer">Read-only</option>
				<option value="developer">Developer</option>
				<option value="admin">Admin</option>
			</select>
			<button type="button" onclick={create} disabled={creating} class="rounded bg-eurobase-600 px-2.5 py-1 font-medium text-white hover:bg-eurobase-700 disabled:opacity-50 cursor-pointer">{creating ? 'Creating…' : 'Create token'}</button>
		</div>
		<p class="mt-1.5 text-gray-500">
			Read-only reads tables, rows and files; Developer can also run SQL and change tables. Not more than your own role here.
		</p>
		{#if error}<p class="mt-1.5 text-red-600">{error}</p>{/if}
	{/if}
</div>
