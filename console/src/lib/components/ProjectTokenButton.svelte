<script lang="ts">
	// Creates a project-scoped personal access token (#702) right where it's
	// needed (Connect page MCP setups), so users don't have to detour to the
	// Account page. The token is shown once.
	import { api } from '$lib/api';

	let {
		projectId,
		projectName = '',
		defaultRole = 'viewer',
		tokenName = 'mcp',
		configTemplate = ''
	}: {
		projectId: string;
		projectName?: string;
		defaultRole?: 'viewer' | 'developer';
		tokenName?: string;
		/** The MCP config shown next to this control; after creation it is
		 *  shown again with the token filled in (placeholders replaced). */
		configTemplate?: string;
	} = $props();

	// The default only picks the starting choice; the user changes it here.
	// svelte-ignore state_referenced_locally
	let role = $state<'viewer' | 'developer'>(defaultRole);
	let copyFailed = $state(false);

	// Admin isn't offered here: it adds secrets, end users and API keys —
	// more than a coding tool should get. The Account page offers it.

	let creating = $state(false);
	let token = $state('');
	let error = $state('');
	let copied = $state(false);
	const PLACEHOLDERS = ['YOUR_EUROBASE_PAT', 'YOUR_PLATFORM_TOKEN', '$EUROBASE_PAT'];
	let filledConfig = $derived(
		token && configTemplate ? PLACEHOLDERS.reduce((c, p) => c.split(p).join(token), configTemplate) : ''
	);

	async function create() {
		creating = true;
		error = '';
		try {
			// Distinguishable in the token list: tool · project · date.
			const date = new Date().toISOString().slice(0, 10);
			const name = [tokenName, projectName, date].filter(Boolean).join(' · ').slice(0, 100);
			const res = await api.createPAT(name, projectId, role);
			token = res.token;
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to create the token';
		} finally {
			creating = false;
		}
	}

	async function copy(text: string) {
		copyFailed = false;
		try {
			await navigator.clipboard.writeText(text);
			copied = true;
			setTimeout(() => (copied = false), 2000);
		} catch {
			copyFailed = true;
		}
	}

	function done() {
		token = '';
		copied = false;
		copyFailed = false;
	}
</script>

<div class="mt-3 rounded-lg border border-gray-200 bg-white p-3 text-xs">
	{#if token}
		<p class="font-medium text-gray-900">Your token — copy it now, it's shown only once</p>
		<div class="mt-2 flex items-center gap-2">
			<code class="flex-1 overflow-x-auto whitespace-nowrap rounded bg-gray-900 px-2 py-1.5 font-mono text-gray-100">{token}</code>
			<button type="button" onclick={() => copy(token)} class="rounded border border-gray-300 px-2 py-1 font-medium text-gray-700 hover:bg-gray-50 cursor-pointer">{copied ? 'Copied!' : 'Copy'}</button>
		</div>
		{#if filledConfig}
			<p class="mt-3 font-medium text-gray-900">Or the config above, with the token filled in:</p>
			<div class="mt-1 flex items-start gap-2">
				<pre class="flex-1 overflow-x-auto rounded bg-gray-900 px-2 py-1.5 font-mono text-gray-100">{filledConfig}</pre>
				<button type="button" onclick={() => copy(filledConfig)} class="rounded border border-gray-300 px-2 py-1 font-medium text-gray-700 hover:bg-gray-50 cursor-pointer">Copy</button>
			</div>
		{/if}
		{#if copyFailed}<p class="mt-1.5 text-amber-700">Copy failed — select the text and copy it by hand.</p>{/if}
		<p class="mt-2 text-gray-500">
			It works only for this project. A previous token keeps working until you revoke it in <a href="/account" class="text-eurobase-700 hover:underline">Account → Tokens</a>.
			<button type="button" onclick={done} class="ml-1 font-medium text-eurobase-700 hover:underline cursor-pointer">Done</button>
		</p>
	{:else}
		<div class="flex flex-wrap items-center gap-2">
			<span class="text-gray-700">Create a token for this project:</span>
			<select bind:value={role} class="rounded border border-gray-300 px-2 py-1 text-xs text-gray-900">
				<option value="viewer">Read-only</option>
				<option value="developer">Developer</option>
			</select>
			<button type="button" onclick={create} disabled={creating} class="rounded bg-eurobase-600 px-2.5 py-1 font-medium text-white hover:bg-eurobase-700 disabled:opacity-50 cursor-pointer">{creating ? 'Creating…' : 'Create token'}</button>
		</div>
		<p class="mt-1.5 text-gray-500">
			Read-only reads tables, rows and files; Developer can also run SQL and change tables. Not more than your own role here. (Admin tokens — secrets, end users, API keys — are made on the Account page.)
		</p>
		{#if error}<p class="mt-1.5 text-red-600">{error}</p>{/if}
	{/if}
</div>
