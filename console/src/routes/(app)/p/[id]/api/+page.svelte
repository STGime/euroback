<script lang="ts">
	import { page } from '$app/stores';
	import { getContext, onMount } from 'svelte';
	import { api, type Project, type TableSchema } from '$lib/api.js';

	const projectCtx = getContext<{ id: string; project: Project | null }>('projectId');
	let projectId = $derived($page.params.id);

	let tables: TableSchema[] = $state([]);
	let loading = $state(true);
	let selectedTable: string | null = $state(null);
	let copiedSnippet: string | null = $state(null);

	onMount(async () => {
		try {
			tables = await api.getSchema(projectId);
			if (tables.length > 0) selectedTable = tables[0].name;
		} catch { /* ignore */ }
		loading = false;
	});

	let selectedSchema = $derived(tables.find(t => t.name === selectedTable) ?? null);

	let endpoints = $derived(selectedTable ? [
		{ method: 'GET', path: `/v1/db/${selectedTable}`, desc: 'List rows with filtering, sorting, pagination', color: 'bg-green-100 text-green-700' },
		{ method: 'GET', path: `/v1/db/${selectedTable}/{id}`, desc: 'Get a single row by ID', color: 'bg-green-100 text-green-700' },
		{ method: 'POST', path: `/v1/db/${selectedTable}`, desc: 'Insert a new row', color: 'bg-blue-100 text-blue-700' },
		{ method: 'PATCH', path: `/v1/db/${selectedTable}/{id}`, desc: 'Update a row by ID', color: 'bg-amber-100 text-amber-700' },
		{ method: 'DELETE', path: `/v1/db/${selectedTable}/{id}`, desc: 'Delete a row by ID', color: 'bg-red-100 text-red-700' },
	] : []);

	function sdkSnippet(table: string): string {
		const slug = projectCtx.project?.slug ?? 'my-project';
		return `import { createClient } from '@eurobase/sdk'

const eb = createClient({
  url: 'https://${slug}.eurobase.app',
  apiKey: 'eb_pk_your_public_key'
})

// List all rows
const { data } = await eb.db.from('${table}').select('*')

// Insert a row
await eb.db.from('${table}').insert({
  // your data here
})

// Filter rows
const filtered = await eb.db
  .from('${table}')
  .select('*')
  .eq('id', 'some-uuid')`;
	}

	function curlSnippet(table: string): string {
		return `# List rows
curl -H "Authorization: Bearer YOUR_TOKEN" \\
     -H "X-Project-Id: ${projectId}" \\
     "${projectCtx.project?.api_url ?? 'http://localhost:8080'}/v1/db/${table}?limit=20"

# Insert a row
curl -X POST \\
     -H "Authorization: Bearer YOUR_TOKEN" \\
     -H "X-Project-Id: ${projectId}" \\
     -H "Content-Type: application/json" \\
     -d '{"key": "value"}' \\
     "${projectCtx.project?.api_url ?? 'http://localhost:8080'}/v1/db/${table}"`;
	}

	let activeTab: 'sdk' | 'curl' = $state('curl');

	function copyCode(code: string, id: string) {
		navigator.clipboard.writeText(code);
		copiedSnippet = id;
		setTimeout(() => { if (copiedSnippet === id) copiedSnippet = null; }, 1500);
	}
</script>

<div class="mx-auto max-w-5xl space-y-6">
	<!-- Header -->
	<div>
		<h2 class="text-lg font-semibold text-gray-900">API Explorer</h2>
		<p class="text-sm text-gray-500">Interactive API documentation auto-generated from your schema.</p>
	</div>

	<!-- API Endpoint -->
	{#if projectCtx.project}
		<div class="flex items-center gap-4 rounded-xl border border-gray-200 bg-white px-5 py-4">
			<div class="flex-1">
				<p class="text-xs font-medium uppercase tracking-wider text-gray-400">API Endpoint</p>
				<code class="mt-1 block text-sm font-mono text-gray-900">{projectCtx.project.api_url || `https://${projectCtx.project.slug}.eurobase.app`}</code>
			</div>
			<div>
				<p class="text-xs font-medium uppercase tracking-wider text-gray-400">Required Headers</p>
				<code class="mt-1 block text-xs font-mono text-gray-600">Authorization: Bearer &lt;token&gt;</code>
				<code class="block text-xs font-mono text-gray-600">X-Project-Id: {projectId}</code>
			</div>
		</div>
	{/if}

	<div class="flex gap-6">
		<!-- Table selector sidebar -->
		<div class="w-44 shrink-0">
			<h3 class="text-xs font-semibold uppercase tracking-wider text-gray-400 mb-2">Tables</h3>
			{#if loading}
				{#each Array(3) as _}
					<div class="h-8 animate-pulse rounded bg-gray-100 mb-1"></div>
				{/each}
			{:else}
				<div class="space-y-0.5">
					{#each tables as table}
						<button
							type="button"
							class="cursor-pointer w-full text-left rounded-lg px-3 py-2 text-sm transition-colors
								{selectedTable === table.name
									? 'bg-eurobase-50 text-eurobase-700 font-medium'
									: 'text-gray-600 hover:bg-gray-50'}"
							onclick={() => (selectedTable = table.name)}
						>
							{table.name}
						</button>
					{/each}
				</div>
			{/if}
		</div>

		<!-- Main content -->
		<div class="flex-1 min-w-0 space-y-6">
			{#if selectedTable && selectedSchema}
				<!-- Endpoints -->
				<div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
					<div class="px-5 py-3 border-b border-gray-100">
						<h3 class="text-sm font-semibold text-gray-900">Endpoints for <code class="font-mono">{selectedTable}</code></h3>
					</div>
					<div class="divide-y divide-gray-100">
						{#each endpoints as ep}
							<div class="flex items-center gap-3 px-5 py-3">
								<span class="inline-flex rounded px-2 py-0.5 text-[10px] font-bold {ep.color}">{ep.method}</span>
								<code class="text-sm font-mono text-gray-900">{ep.path}</code>
								<span class="ml-auto text-xs text-gray-400">{ep.desc}</span>
							</div>
						{/each}
					</div>
				</div>

				<!-- Schema -->
				<div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
					<div class="px-5 py-3 border-b border-gray-100">
						<h3 class="text-sm font-semibold text-gray-900">Schema</h3>
					</div>
					<table class="w-full">
						<thead>
							<tr class="border-b border-gray-100">
								<th class="px-5 py-2 text-left text-xs font-semibold uppercase tracking-wider text-gray-400">Column</th>
								<th class="px-5 py-2 text-left text-xs font-semibold uppercase tracking-wider text-gray-400">Type</th>
								<th class="px-5 py-2 text-left text-xs font-semibold uppercase tracking-wider text-gray-400">Nullable</th>
								<th class="px-5 py-2 text-left text-xs font-semibold uppercase tracking-wider text-gray-400">Default</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-gray-50">
							{#each selectedSchema.columns as col}
								<tr>
									<td class="px-5 py-2 text-sm font-mono text-gray-900">{col.name}</td>
									<td class="px-5 py-2">
										<span class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-medium text-gray-600">{col.data_type}</span>
									</td>
									<td class="px-5 py-2 text-xs text-gray-500">{col.is_nullable ? 'yes' : 'no'}</td>
									<td class="px-5 py-2 text-xs font-mono text-gray-400">{col.default_value ?? '—'}</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>

				<!-- Query Parameters -->
				<div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
					<div class="px-5 py-3 border-b border-gray-100">
						<h3 class="text-sm font-semibold text-gray-900">Query Parameters</h3>
					</div>
					<div class="divide-y divide-gray-50">
						{#each [
							{ param: 'select', example: '?select=id,name,email', desc: 'Column selection' },
							{ param: 'order', example: '?order=created_at.desc', desc: 'Sort by column' },
							{ param: 'limit', example: '?limit=20', desc: 'Max rows to return (default 20, max 1000)' },
							{ param: 'offset', example: '?offset=40', desc: 'Skip rows for pagination' },
							{ param: '{column}', example: '?name=eq.Stefan', desc: 'Filter: eq, neq, gt, gte, lt, lte, like, ilike, is' },
						] as qp}
							<div class="flex items-baseline gap-4 px-5 py-2.5">
								<code class="text-xs font-mono font-semibold text-eurobase-700 w-20 shrink-0">{qp.param}</code>
								<code class="text-xs font-mono text-gray-500">{qp.example}</code>
								<span class="ml-auto text-xs text-gray-400">{qp.desc}</span>
							</div>
						{/each}
					</div>
				</div>

				<!-- Code snippets -->
				<div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
					<div class="flex items-center justify-between px-5 py-3 border-b border-gray-100">
						<h3 class="text-sm font-semibold text-gray-900">Code Snippets</h3>
						<div class="flex gap-1">
							<button type="button" class="cursor-pointer rounded-md px-3 py-1 text-xs font-medium transition-colors {activeTab === 'curl' ? 'bg-gray-900 text-white' : 'text-gray-500 hover:bg-gray-100'}" onclick={() => (activeTab = 'curl')}>cURL</button>
							<button type="button" class="cursor-pointer rounded-md px-3 py-1 text-xs font-medium transition-colors {activeTab === 'sdk' ? 'bg-gray-900 text-white' : 'text-gray-500 hover:bg-gray-100'}" onclick={() => (activeTab = 'sdk')}>SDK</button>
						</div>
					</div>
					<div class="relative">
						<pre class="p-5 text-sm font-mono text-green-400 bg-gray-900 overflow-x-auto">{activeTab === 'sdk' ? sdkSnippet(selectedTable) : curlSnippet(selectedTable)}</pre>
						<button
							type="button"
							class="cursor-pointer absolute top-3 right-3 rounded-md bg-gray-800 px-2 py-1 text-[10px] font-medium text-gray-400 hover:text-white transition-colors"
							onclick={() => copyCode(activeTab === 'sdk' ? sdkSnippet(selectedTable!) : curlSnippet(selectedTable!), activeTab)}
						>
							{copiedSnippet === activeTab ? 'Copied!' : 'Copy'}
						</button>
					</div>
				</div>
			{:else if !loading}
				<div class="rounded-xl border border-gray-200 bg-white p-12 text-center">
					<p class="text-sm text-gray-400">Create a table to see API documentation.</p>
				</div>
			{/if}
		</div>
	</div>
</div>
