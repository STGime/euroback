<script lang="ts">
	import { page } from '$app/stores';
	import { browser } from '$app/environment';
	import { api, type ConnectInfo } from '$lib/api.js';

	type IdeTab = 'claude' | 'cursor' | 'windsurf' | 'generic';
	const STORAGE_KEY = 'eurobase:connect-tab';

	function loadSavedTab(): IdeTab {
		if (browser) {
			const saved = localStorage.getItem(STORAGE_KEY);
			if (saved === 'claude' || saved === 'cursor' || saved === 'windsurf' || saved === 'generic') return saved;
		}
		return 'claude';
	}

	let projectId = $derived($page.params.id);
	let info = $state<ConnectInfo | null>(null);
	let loading = $state(true);
	let error = $state<string | null>(null);
	let activeTab = $state<IdeTab>(loadSavedTab());
	let copiedField = $state('');

	function setTab(tab: IdeTab) {
		activeTab = tab;
		if (browser) localStorage.setItem(STORAGE_KEY, tab);
	}

	$effect(() => {
		loadConnect();
	});

	async function loadConnect() {
		loading = true;
		error = null;
		try {
			const hiddenTables = new Set(['users', 'refresh_tokens', 'storage_objects', 'email_tokens', 'vault_secrets']);
			info = await api.getConnectInfo(projectId);
			info.tables = info.tables.filter(t => !hiddenTables.has(t.name));
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load connect info';
		} finally {
			loading = false;
		}
	}

	async function copyToClipboard(text: string, field: string) {
		try {
			await navigator.clipboard.writeText(text);
			copiedField = field;
			setTimeout(() => { copiedField = ''; }, 2000);
		} catch {
			// silently fail
		}
	}

	function downloadFile(filename: string, content: string) {
		const blob = new Blob([content], { type: 'text/plain' });
		const url = URL.createObjectURL(blob);
		const a = document.createElement('a');
		a.href = url;
		a.download = filename;
		a.click();
		URL.revokeObjectURL(url);
	}
</script>

<svelte:head>
	<title>Connect - Eurobase Console</title>
</svelte:head>

{#if loading}
	<div class="space-y-4">
		<div class="h-8 w-48 animate-pulse rounded bg-gray-200"></div>
		<div class="h-64 animate-pulse rounded-xl bg-gray-100"></div>
	</div>
{:else if error}
	<div class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">
		{error}
		<button onclick={loadConnect} class="ml-2 font-medium underline cursor-pointer">Retry</button>
	</div>
{:else if info}
	<div>
		<h2 class="text-lg font-bold text-gray-900">Connect your IDE</h2>
		<p class="mt-1 text-sm text-gray-500">
			Download configuration files pre-filled with your project's URL and schema. Drop them into your project directory.
		</p>

		<!-- IDE tabs -->
		<div class="mt-6 border-b border-gray-200">
			<nav class="flex gap-6" aria-label="IDE tabs">
				{#each [
					{ id: 'claude', label: 'Claude Code' },
					{ id: 'cursor', label: 'Cursor' },
					{ id: 'windsurf', label: 'Windsurf' },
					{ id: 'generic', label: 'Generic' }
				] as tab}
					<button
						onclick={() => setTab(tab.id as IdeTab)}
						class="border-b-2 pb-3 text-sm font-medium transition-colors cursor-pointer {activeTab === tab.id ? 'border-eurobase-600 text-eurobase-700' : 'border-transparent text-gray-500 hover:text-gray-700 hover:border-gray-300'}"
					>
						{tab.label}
					</button>
				{/each}
			</nav>
		</div>

		<div class="mt-6">
			{#if activeTab === 'claude'}
				<div class="space-y-4">
					<div class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
						<div class="flex items-center justify-between mb-3">
							<div>
								<p class="text-sm font-semibold text-gray-900">CLAUDE.md</p>
								<p class="text-xs text-gray-500">Drop this into your project root. Claude Code will use it for context.</p>
							</div>
							<div class="flex gap-2">
								<button
									onclick={() => copyToClipboard(info.claude_md, 'claude')}
									class="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors cursor-pointer"
								>
									{copiedField === 'claude' ? 'Copied!' : 'Copy'}
								</button>
								<button
									onclick={() => downloadFile('CLAUDE.md', info.claude_md)}
									class="rounded-md bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 transition-colors cursor-pointer"
								>
									Download
								</button>
							</div>
						</div>
						<pre class="rounded-lg bg-gray-50 border border-gray-100 p-4 text-xs font-mono text-gray-700 overflow-x-auto max-h-80 overflow-y-auto">{info.claude_md}</pre>
					</div>

					<div class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
						<div class="flex items-center justify-between mb-3">
							<div>
								<p class="text-sm font-semibold text-gray-900">.env</p>
								<p class="text-xs text-gray-500">Environment variables for your project</p>
							</div>
							<button
								onclick={() => downloadFile('.env', info.env_template)}
								class="rounded-md bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 transition-colors cursor-pointer"
							>
								Download
							</button>
						</div>
						<pre class="rounded-lg bg-gray-50 border border-gray-100 p-4 text-xs font-mono text-gray-700 overflow-x-auto">{info.env_template}</pre>
					</div>
				</div>

			{:else if activeTab === 'cursor'}
				<div class="space-y-4">
					<div class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
						<div class="flex items-center justify-between mb-3">
							<div>
								<p class="text-sm font-semibold text-gray-900">.cursorrules</p>
								<p class="text-xs text-gray-500">Cursor IDE context rules for your Eurobase project</p>
							</div>
							<div class="flex gap-2">
								<button
									onclick={() => copyToClipboard(info.cursor_rules, 'cursor')}
									class="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors cursor-pointer"
								>
									{copiedField === 'cursor' ? 'Copied!' : 'Copy'}
								</button>
								<button
									onclick={() => downloadFile('.cursorrules', info.cursor_rules)}
									class="rounded-md bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 transition-colors cursor-pointer"
								>
									Download
								</button>
							</div>
						</div>
						<pre class="rounded-lg bg-gray-50 border border-gray-100 p-4 text-xs font-mono text-gray-700 overflow-x-auto max-h-80 overflow-y-auto">{info.cursor_rules}</pre>
					</div>

					<div class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
						<div class="flex items-center justify-between mb-3">
							<div>
								<p class="text-sm font-semibold text-gray-900">.env</p>
								<p class="text-xs text-gray-500">Environment variables for your project</p>
							</div>
							<button
								onclick={() => downloadFile('.env', info.env_template)}
								class="rounded-md bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 transition-colors cursor-pointer"
							>
								Download
							</button>
						</div>
						<pre class="rounded-lg bg-gray-50 border border-gray-100 p-4 text-xs font-mono text-gray-700 overflow-x-auto">{info.env_template}</pre>
					</div>
				</div>

			{:else if activeTab === 'windsurf'}
				<div class="space-y-4">
					<div class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
						<div class="flex items-center justify-between mb-3">
							<div>
								<p class="text-sm font-semibold text-gray-900">.windsurfrules</p>
								<p class="text-xs text-gray-500">Windsurf context rules (same format as Cursor)</p>
							</div>
							<div class="flex gap-2">
								<button
									onclick={() => copyToClipboard(info.cursor_rules, 'windsurf')}
									class="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors cursor-pointer"
								>
									{copiedField === 'windsurf' ? 'Copied!' : 'Copy'}
								</button>
								<button
									onclick={() => downloadFile('.windsurfrules', info.cursor_rules)}
									class="rounded-md bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 transition-colors cursor-pointer"
								>
									Download
								</button>
							</div>
						</div>
						<pre class="rounded-lg bg-gray-50 border border-gray-100 p-4 text-xs font-mono text-gray-700 overflow-x-auto max-h-80 overflow-y-auto">{info.cursor_rules}</pre>
					</div>
				</div>

			{:else if activeTab === 'generic'}
				<div class="space-y-4">
					<!-- Install SDK -->
					<div class="rounded-xl border border-eurobase-200 bg-eurobase-50/30 p-5 shadow-sm">
						<div class="flex items-center justify-between mb-3">
							<div>
								<p class="text-sm font-semibold text-gray-900">1. Install the SDK</p>
								<p class="text-xs text-gray-500">Add the Eurobase JavaScript SDK to your project</p>
							</div>
							<button
								onclick={() => copyToClipboard('npm install @eurobase/sdk', 'install')}
								class="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors cursor-pointer"
							>
								{copiedField === 'install' ? 'Copied!' : 'Copy'}
							</button>
						</div>
						<pre class="rounded-lg bg-gray-900 px-4 py-3 text-sm font-mono text-gray-100 overflow-x-auto">npm install @eurobase/sdk</pre>
					</div>

					<!-- .env setup -->
					<div class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
						<div class="flex items-center justify-between mb-3">
							<div>
								<p class="text-sm font-semibold text-gray-900">2. Set up environment variables</p>
								<p class="text-xs text-gray-500">Create a <code class="text-xs bg-gray-100 rounded px-1">.env</code> file in your project root. Get your API keys from <a href="/p/{info.project_id}/settings" class="text-eurobase-600 hover:underline">Settings</a>.</p>
							</div>
							<button
								onclick={() => downloadFile('.env', info.env_template)}
								class="rounded-md bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 transition-colors cursor-pointer"
							>
								Download .env
							</button>
						</div>
						<pre class="rounded-lg bg-gray-900 px-4 py-3 text-xs font-mono text-gray-100 overflow-x-auto">{info.env_template}</pre>
					</div>

					<!-- Connection string -->
					<div class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
						<div class="flex items-center justify-between mb-3">
							<div>
								<p class="text-sm font-semibold text-gray-900">Connection String</p>
								<p class="text-xs text-gray-500">Alternative: pass a single URL instead of separate url + apiKey</p>
							</div>
						</div>
						<code class="block rounded-lg bg-gray-900 px-4 py-3 text-sm font-mono text-gray-100 overflow-x-auto">eurobase://<span class="text-amber-400">YOUR_PUBLIC_KEY</span>@{info.slug}.eurobase.app</code>
					</div>

					{#if info.sample_code.javascript}
						<div class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
							<div class="flex items-center justify-between mb-3">
								<p class="text-sm font-semibold text-gray-900">3. Use the SDK</p>
								<button
									onclick={() => copyToClipboard(info.sample_code.javascript, 'js')}
									class="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors cursor-pointer"
								>
									{copiedField === 'js' ? 'Copied!' : 'Copy'}
								</button>
							</div>
							<pre class="rounded-lg bg-gray-900 p-4 text-xs font-mono text-gray-100 overflow-x-auto max-h-60 overflow-y-auto">{info.sample_code.javascript}</pre>
						</div>
					{/if}

					{#if info.sample_code.curl}
						<div class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
							<div class="flex items-center justify-between mb-3">
								<p class="text-sm font-semibold text-gray-900">Or use cURL directly</p>
								<button
									onclick={() => copyToClipboard(info.sample_code.curl, 'curl')}
									class="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors cursor-pointer"
								>
									{copiedField === 'curl' ? 'Copied!' : 'Copy'}
								</button>
							</div>
							<pre class="rounded-lg bg-gray-900 p-4 text-xs font-mono text-gray-100 overflow-x-auto">{info.sample_code.curl}</pre>
						</div>
					{/if}
				</div>
			{/if}
		</div>

		<!-- Schema overview -->
		{#if info.tables.length > 0}
			<div class="mt-8">
				<h3 class="text-sm font-semibold text-gray-900">Available Tables</h3>
				<div class="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
					{#each info.tables as table}
						<div class="rounded-lg border border-gray-200 bg-white p-3">
							<p class="text-sm font-medium text-gray-900">{table.name}</p>
							<p class="mt-1 text-xs text-gray-500">{table.columns.length} columns</p>
							<div class="mt-2 space-y-0.5">
								{#each table.columns as col}
									<div class="flex items-center gap-2 text-xs">
										<span class="font-mono text-gray-700">{col.name}</span>
										<span class="text-gray-400">{col.data_type}</span>
									</div>
								{/each}
							</div>
						</div>
					{/each}
				</div>
			</div>
		{/if}
	</div>
{/if}
