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
			const hiddenTables = new Set(['users', 'refresh_tokens', 'storage_objects', 'email_tokens', 'vault_secrets']);
			tables = (await api.getSchema(projectId)).filter(t => !hiddenTables.has(t.name));
			if (tables.length > 0) selectedTable = tables[0].name;
		} catch { /* ignore */ }
		loading = false;
	});

	let selectedSchema = $derived(tables.find(t => t.name === selectedTable) ?? null);

	let endpoints = $derived(selectedTable ? [
		{ method: 'GET', path: `/v1/db/${selectedTable}`, desc: 'List rows with filtering, sorting, pagination', color: 'bg-green-100 text-green-700', kind: 'list' as const },
		{ method: 'GET', path: `/v1/db/${selectedTable}/{id}`, desc: 'Get a single row by ID', color: 'bg-green-100 text-green-700', kind: 'get' as const },
		{ method: 'POST', path: `/v1/db/${selectedTable}`, desc: 'Insert a new row', color: 'bg-blue-100 text-blue-700', kind: 'insert' as const },
		{ method: 'PATCH', path: `/v1/db/${selectedTable}/{id}`, desc: 'Update a row by ID', color: 'bg-amber-100 text-amber-700', kind: 'update' as const },
		{ method: 'DELETE', path: `/v1/db/${selectedTable}/{id}`, desc: 'Delete a row by ID', color: 'bg-red-100 text-red-700', kind: 'delete' as const },
	] : []);

	// Try-it panel state
	let expandedEndpoint: string | null = $state(null);
	let deleteConfirmed = $state(false);
	let tryLimit = $state('10');
	let tryOffset = $state('0');
	let tryOrder = $state('');
	let trySelect = $state('');
	let tryRowId = $state('');
	let tryBody = $state('{\n  \n}');
	let tryResult: string | null = $state(null);
	let tryError: string | null = $state(null);
	let trySending = $state(false);
	let tryStatusCode: number | null = $state(null);
	let tryDuration: number | null = $state(null);

	function toggleEndpoint(kind: string) {
		if (expandedEndpoint === kind) {
			expandedEndpoint = null;
		} else {
			expandedEndpoint = kind;
			tryResult = null;
			tryError = null;
			tryStatusCode = null;
			tryDuration = null;
			tryRowId = '';
			tryBody = '{\n  \n}';
			deleteConfirmed = false;
		}
	}

	function resetTryState() {
		tryResult = null;
		tryError = null;
		tryStatusCode = null;
		tryDuration = null;
	}

	async function executeTry(kind: string) {
		if (!selectedTable) return;
		resetTryState();
		trySending = true;
		const start = performance.now();

		try {
			let result: any;

			if (kind === 'list') {
				const params: Record<string, any> = {};
				if (tryLimit) params.limit = parseInt(tryLimit);
				if (tryOffset && tryOffset !== '0') params.offset = parseInt(tryOffset);
				if (tryOrder) params.order = tryOrder;
				if (trySelect) params.select = trySelect;
				result = await api.queryTable(projectId, selectedTable, params);
				tryStatusCode = 200;
			} else if (kind === 'get') {
				if (!tryRowId.trim()) {
					tryError = 'Row ID is required';
					trySending = false;
					return;
				}
				result = await api.queryTable(projectId, selectedTable, {
					filters: { id: `eq.${tryRowId.trim()}` },
					limit: 1
				});
				tryStatusCode = 200;
			} else if (kind === 'insert') {
				let body: Record<string, any>;
				try {
					body = JSON.parse(tryBody);
				} catch {
					tryError = 'Invalid JSON body';
					trySending = false;
					return;
				}
				result = await api.insertRow(projectId, selectedTable, body);
				tryStatusCode = 201;
			} else if (kind === 'update') {
				if (!tryRowId.trim()) {
					tryError = 'Row ID is required';
					trySending = false;
					return;
				}
				let body: Record<string, any>;
				try {
					body = JSON.parse(tryBody);
				} catch {
					tryError = 'Invalid JSON body';
					trySending = false;
					return;
				}
				result = await api.updateRow(projectId, selectedTable, tryRowId.trim(), body);
				tryStatusCode = 200;
			} else if (kind === 'delete') {
				if (!tryRowId.trim()) {
					tryError = 'Row ID is required';
					trySending = false;
					return;
				}
				await api.deleteRow(projectId, selectedTable, tryRowId.trim());
				result = { status: 'deleted' };
				tryStatusCode = 204;
			}

			tryDuration = Math.round(performance.now() - start);
			tryResult = JSON.stringify(result, null, 2);
		} catch (err) {
			tryDuration = Math.round(performance.now() - start);
			const msg = err instanceof Error ? err.message : String(err);
			const statusMatch = msg.match(/API (\d+)/);
			tryStatusCode = statusMatch ? parseInt(statusMatch[1]) : 500;
			tryError = msg;
		} finally {
			trySending = false;
		}
	}

	function needsId(kind: string): boolean {
		return kind === 'get' || kind === 'update' || kind === 'delete';
	}

	function needsBody(kind: string): boolean {
		return kind === 'insert' || kind === 'update';
	}

	function needsQueryParams(kind: string): boolean {
		return kind === 'list';
	}

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
  .eq('id', 'some-uuid')

// ── Authentication ──

// Sign up a new user
const { data: session, error } = await eb.auth.signUp({
  email: 'user@example.com', password: 'securepassword'
})

// Sign in
await eb.auth.signIn({ email: 'user@example.com', password: 'securepassword' })

// After sign-in, JWT is sent automatically with every query (RLS enforced)
eb.auth.onAuthStateChange((event, session) => {
  console.log(event) // SIGNED_IN | SIGNED_OUT | TOKEN_REFRESHED
})

// Sign out
await eb.auth.signOut()`;
	}

	function curlSnippet(table: string): string {
		return `# List rows
curl -H "apikey: YOUR_PUBLIC_KEY" \\
     "${projectCtx.project?.api_url ?? 'http://localhost:8080'}/v1/db/${table}?limit=20"

# List rows (as authenticated end-user)
curl -H "apikey: YOUR_PUBLIC_KEY" \\
     -H "Authorization: Bearer END_USER_JWT" \\
     "${projectCtx.project?.api_url ?? 'http://localhost:8080'}/v1/db/${table}?limit=20"

# Insert a row
curl -X POST \\
     -H "apikey: YOUR_PUBLIC_KEY" \\
     -H "Content-Type: application/json" \\
     -d '{"key": "value"}' \\
     "${projectCtx.project?.api_url ?? 'http://localhost:8080'}/v1/db/${table}"

# End-user auth — sign up
curl -X POST \\
     -H "apikey: YOUR_PUBLIC_KEY" \\
     -H "Content-Type: application/json" \\
     -d '{"email": "user@example.com", "password": "secret"}' \\
     "${projectCtx.project?.api_url ?? 'http://localhost:8080'}/v1/auth/signup"

# End-user auth — sign in
curl -X POST \\
     -H "apikey: YOUR_PUBLIC_KEY" \\
     -H "Content-Type: application/json" \\
     -d '{"email": "user@example.com", "password": "secret"}' \\
     "${projectCtx.project?.api_url ?? 'http://localhost:8080'}/v1/auth/signin"`;
	}

	let activeTab: 'sdk' | 'curl' = $state('curl');

	function copyCode(code: string, id: string) {
		navigator.clipboard.writeText(code);
		copiedSnippet = id;
		setTimeout(() => { if (copiedSnippet === id) copiedSnippet = null; }, 1500);
	}

	function statusColor(code: number | null): string {
		if (!code) return 'text-gray-400';
		if (code < 300) return 'text-green-600';
		if (code < 400) return 'text-amber-600';
		return 'text-red-600';
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
				<p class="text-xs font-medium uppercase tracking-wider text-gray-400">Required Header</p>
				<code class="mt-1 block text-xs font-mono text-gray-600">apikey: &lt;your-public-key&gt;</code>
				<p class="text-xs font-medium uppercase tracking-wider text-gray-400 mt-2">Optional Header</p>
				<code class="mt-1 block text-xs font-mono text-gray-600">Authorization: Bearer &lt;end-user-jwt&gt;</code>
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
							onclick={() => { selectedTable = table.name; expandedEndpoint = null; resetTryState(); }}
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
				<!-- Endpoints with Try It -->
				<div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
					<div class="px-5 py-3 border-b border-gray-100">
						<h3 class="text-sm font-semibold text-gray-900">Endpoints for <code class="font-mono">{selectedTable}</code></h3>
					</div>
					<div class="divide-y divide-gray-100">
						{#each endpoints as ep}
							<div>
								<button
									type="button"
									class="cursor-pointer flex items-center gap-3 px-5 py-3 w-full text-left hover:bg-gray-50 transition-colors"
									onclick={() => toggleEndpoint(ep.kind)}
								>
									<span class="inline-flex rounded px-2 py-0.5 text-[10px] font-bold {ep.color}">{ep.method}</span>
									<code class="text-sm font-mono text-gray-900">{ep.path}</code>
									<span class="ml-auto text-xs text-gray-400 mr-2">{ep.desc}</span>
									<svg class="h-4 w-4 text-gray-400 shrink-0 transition-transform {expandedEndpoint === ep.kind ? 'rotate-180' : ''}" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
										<path stroke-linecap="round" stroke-linejoin="round" d="m19.5 8.25-7.5 7.5-7.5-7.5" />
									</svg>
								</button>

								{#if expandedEndpoint === ep.kind}
									<div class="px-5 pb-4 pt-1 border-t border-gray-50 bg-gray-50/50">
										<div class="space-y-3">
											<!-- Query params for list -->
											{#if needsQueryParams(ep.kind)}
												<div class="grid grid-cols-4 gap-2">
													<div>
														<label class="block text-[10px] font-medium text-gray-500 mb-1">select</label>
														<input type="text" bind:value={trySelect} placeholder="*" class="w-full rounded border border-gray-300 px-2 py-1.5 text-xs font-mono focus:border-eurobase-500 focus:ring-1 focus:ring-eurobase-500 outline-none" />
													</div>
													<div>
														<label class="block text-[10px] font-medium text-gray-500 mb-1">order</label>
														<input type="text" bind:value={tryOrder} placeholder="created_at.desc" class="w-full rounded border border-gray-300 px-2 py-1.5 text-xs font-mono focus:border-eurobase-500 focus:ring-1 focus:ring-eurobase-500 outline-none" />
													</div>
													<div>
														<label class="block text-[10px] font-medium text-gray-500 mb-1">limit</label>
														<input type="text" bind:value={tryLimit} placeholder="10" class="w-full rounded border border-gray-300 px-2 py-1.5 text-xs font-mono focus:border-eurobase-500 focus:ring-1 focus:ring-eurobase-500 outline-none" />
													</div>
													<div>
														<label class="block text-[10px] font-medium text-gray-500 mb-1">offset</label>
														<input type="text" bind:value={tryOffset} placeholder="0" class="w-full rounded border border-gray-300 px-2 py-1.5 text-xs font-mono focus:border-eurobase-500 focus:ring-1 focus:ring-eurobase-500 outline-none" />
													</div>
												</div>
											{/if}

											<!-- Row ID input -->
											{#if needsId(ep.kind)}
												<div>
													<label class="block text-[10px] font-medium text-gray-500 mb-1">Row ID</label>
													<input type="text" bind:value={tryRowId} placeholder="uuid" class="w-full rounded border border-gray-300 px-2 py-1.5 text-xs font-mono focus:border-eurobase-500 focus:ring-1 focus:ring-eurobase-500 outline-none" />
												</div>
											{/if}

											<!-- JSON body -->
											{#if needsBody(ep.kind)}
												<div>
													<label class="block text-[10px] font-medium text-gray-500 mb-1">Request Body (JSON)</label>
													<textarea
														bind:value={tryBody}
														rows={5}
														class="w-full rounded border border-gray-300 px-3 py-2 text-xs font-mono focus:border-eurobase-500 focus:ring-1 focus:ring-eurobase-500 outline-none resize-y"
													></textarea>
													{#if selectedSchema}
														<p class="text-[10px] text-gray-400 mt-1">
															Columns: {selectedSchema.columns.map(c => c.name).join(', ')}
														</p>
													{/if}
												</div>
											{/if}

											<!-- Warning for write operations -->
											{#if ep.kind === 'delete'}
												<div class="flex items-start gap-2 rounded-lg border border-red-200 bg-red-50 px-3 py-2">
													<svg class="h-4 w-4 shrink-0 text-red-500 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
														<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z" />
													</svg>
													<p class="text-xs text-red-700">This will permanently delete real data from your database. This action cannot be undone.</p>
												</div>
											{:else if ep.kind === 'insert' || ep.kind === 'update'}
												<div class="flex items-start gap-2 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2">
													<svg class="h-4 w-4 shrink-0 text-amber-500 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
														<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
													</svg>
													<p class="text-xs text-amber-700">This executes against your live database. Rows will be {ep.kind === 'insert' ? 'created' : 'modified'} in your project.</p>
												</div>
											{/if}

											<!-- Send button -->
											<div class="flex items-center gap-3">
												{#if ep.kind === 'delete' && !deleteConfirmed}
												<button
													type="button"
													onclick={() => { deleteConfirmed = true; }}
													class="cursor-pointer inline-flex items-center gap-1.5 rounded-lg bg-red-600 px-4 py-2 text-xs font-medium text-white hover:bg-red-700 transition-colors"
												>
													<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
														<path stroke-linecap="round" stroke-linejoin="round" d="m14.74 9-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 0 1-2.244 2.077H8.084a2.25 2.25 0 0 1-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 0 0-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 0 1 3.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 0 0-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 0 0-7.5 0" />
													</svg>
													I understand, allow delete
												</button>
												{:else}
												<button
													type="button"
													disabled={trySending}
													onclick={() => executeTry(ep.kind)}
													class="cursor-pointer inline-flex items-center gap-1.5 rounded-lg {ep.kind === 'delete' ? 'bg-red-600 hover:bg-red-700' : 'bg-eurobase-600 hover:bg-eurobase-700'} px-4 py-2 text-xs font-medium text-white transition-colors disabled:opacity-50"
												>
													{#if trySending}
														<svg class="h-3.5 w-3.5 animate-spin" fill="none" viewBox="0 0 24 24">
															<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
															<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
														</svg>
														Sending...
													{:else}
														<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
															<path stroke-linecap="round" stroke-linejoin="round" d="M6 12 3.269 3.125A59.769 59.769 0 0 1 21.485 12 59.768 59.768 0 0 1 3.27 20.875L5.999 12Zm0 0h7.5" />
														</svg>
														Send Request
													{/if}
												</button>
												{/if}
												{#if tryStatusCode !== null}
													<span class="text-xs font-mono {statusColor(tryStatusCode)}">
														{tryStatusCode}
													</span>
													{#if tryDuration !== null}
														<span class="text-xs text-gray-400">{tryDuration}ms</span>
													{/if}
												{/if}
											</div>

											<!-- Response -->
											{#if tryResult || tryError}
												<div class="rounded-lg border border-gray-200 overflow-hidden">
													<div class="px-3 py-1.5 bg-gray-100 border-b border-gray-200 flex items-center justify-between">
														<span class="text-[10px] font-medium text-gray-500 uppercase tracking-wider">Response</span>
														{#if tryResult}
															<button
																type="button"
																class="cursor-pointer text-[10px] text-gray-400 hover:text-gray-600"
																onclick={() => { if (tryResult) copyCode(tryResult, 'response'); }}
															>
																{copiedSnippet === 'response' ? 'Copied!' : 'Copy'}
															</button>
														{/if}
													</div>
													<pre class="p-3 text-xs font-mono overflow-x-auto max-h-80 overflow-y-auto {tryError ? 'text-red-600 bg-red-50' : 'text-gray-800 bg-white'}">{tryError ?? tryResult}</pre>
												</div>
											{/if}
										</div>
									</div>
								{/if}
							</div>
						{/each}
					</div>
				</div>

				<!-- Auth Endpoints -->
				<div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
					<div class="px-5 py-3 border-b border-gray-100">
						<h3 class="text-sm font-semibold text-gray-900">End-User Auth Endpoints</h3>
					</div>
					<div class="divide-y divide-gray-100">
						{#each [
							{ method: 'POST', path: '/v1/auth/signup', desc: 'Register a new end-user', color: 'bg-blue-100 text-blue-700' },
							{ method: 'POST', path: '/v1/auth/signin', desc: 'Sign in an end-user', color: 'bg-blue-100 text-blue-700' },
							{ method: 'POST', path: '/v1/auth/refresh', desc: 'Refresh an end-user JWT', color: 'bg-blue-100 text-blue-700' },
							{ method: 'POST', path: '/v1/auth/signout', desc: 'Sign out (invalidate refresh token)', color: 'bg-blue-100 text-blue-700' },
							{ method: 'GET', path: '/v1/auth/user', desc: 'Get current end-user profile', color: 'bg-green-100 text-green-700' },
						] as ep}
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
