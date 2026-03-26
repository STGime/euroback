<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { getContext } from 'svelte';
	import { api } from '$lib/api.js';

	let projectId = $derived($page.params.id);
	let ctx: { project: import('$lib/api.js').Project | null } = getContext('projectId');
	let project = $derived(ctx.project);

	let tableCount = $state('—');
	let requestCount = $state('—');

	onMount(async () => {
		try {
			const [schema, logs] = await Promise.all([
				api.getSchema(projectId),
				api.getLogs(projectId, { limit: 1 })
			]);
			const hiddenTables = new Set(['users', 'refresh_tokens', 'storage_objects', 'email_tokens']);
			tableCount = String(schema.filter(t => !hiddenTables.has(t.name)).length);
			requestCount = logs.stats.total_requests.toLocaleString();
		} catch {
			// Keep placeholder values on error.
		}
	});

	let stats = $derived([
		{ label: 'Tables', value: tableCount, icon: 'table' },
		{ label: 'Storage used', value: '—', icon: 'storage' },
		{ label: 'API requests', value: requestCount, icon: 'api' }
	]);

	let quickActions = $derived([
		{ label: 'Open Database', href: `/p/${projectId}/database`, color: 'eurobase' },
		{ label: 'Open Storage', href: `/p/${projectId}/storage`, color: 'emerald' },
		{ label: 'View API Keys', href: `/p/${projectId}/settings`, color: 'violet' }
	]);

	let copiedStep: string | null = $state(null);
	let guideDismissed = $state(false);

	function copyCode(code: string, id: string) {
		navigator.clipboard.writeText(code);
		copiedStep = id;
		setTimeout(() => { if (copiedStep === id) copiedStep = null; }, 1500);
	}

	let apiUrl = $derived(project?.api_url || `https://${project?.slug}.eurobase.app`);

	let getStartedSteps = $derived([
		{
			id: 'install',
			title: 'Install the SDK',
			desc: 'Add the Eurobase JavaScript SDK to your project.',
			code: 'npm install @eurobase/sdk',
		},
		{
			id: 'init',
			title: 'Initialize the client',
			desc: 'Create a client with your project URL and public API key.',
			code: `import { createClient } from '@eurobase/sdk'

const eb = createClient({
  url: '${apiUrl}',
  apiKey: process.env.EUROBASE_PUBLIC_KEY
})`,
		},
		{
			id: 'query',
			title: 'Make your first query',
			desc: 'Read data from any table. The SDK handles auth headers automatically.',
			code: `const { data, error } = await eb.db.from('todos').select('*')
console.log(data)`,
		},
		{
			id: 'auth',
			title: 'Add authentication',
			desc: 'Sign up and sign in end-users. After sign-in, the JWT is sent with every query and RLS policies are enforced.',
			code: `// Sign up a new user
await eb.auth.signUp({ email: 'user@example.com', password: 'securepassword' })

// Sign in
await eb.auth.signIn({ email: 'user@example.com', password: 'securepassword' })

// All subsequent queries are now authenticated
const { data } = await eb.db.from('todos').select('*')`,
		},
	]);
</script>

{#if project}
	<!-- Project info card -->
	<div class="rounded-xl border border-gray-200 bg-white p-6 mb-6">
		<div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
			<div>
				<div class="text-xs font-medium uppercase tracking-wider text-gray-400 mb-1">Project</div>
				<div class="text-sm font-semibold text-gray-900">{project.name}</div>
			</div>
			<div>
				<div class="text-xs font-medium uppercase tracking-wider text-gray-400 mb-1">Slug</div>
				<div class="text-sm font-mono text-gray-600">{project.slug}</div>
			</div>
			<div>
				<div class="text-xs font-medium uppercase tracking-wider text-gray-400 mb-1">Region</div>
				<div class="text-sm text-gray-600 flex items-center gap-1.5">
					<svg class="h-3.5 w-3.5 text-eurobase-500" viewBox="0 0 24 24" fill="currentColor">
						<circle cx="12" cy="12" r="10" fill="none" stroke="currentColor" stroke-width="1.5"/>
						<circle cx="12" cy="5" r="0.8" />
						<circle cx="15.5" cy="6.3" r="0.8" />
						<circle cx="8.5" cy="6.3" r="0.8" />
					</svg>
					{project.region}
				</div>
			</div>
			<div>
				<div class="text-xs font-medium uppercase tracking-wider text-gray-400 mb-1">Plan</div>
				<div class="text-sm text-gray-600 capitalize">{project.plan}</div>
			</div>
		</div>
	</div>

	<!-- Stats cards -->
	<div class="grid grid-cols-1 sm:grid-cols-3 gap-4 mb-6">
		{#each stats as stat}
			<div class="rounded-xl border border-gray-200 bg-white p-5">
				<div class="flex items-center gap-3">
					<div class="flex h-10 w-10 items-center justify-center rounded-lg bg-eurobase-50">
						{#if stat.icon === 'table'}
							<svg class="h-5 w-5 text-eurobase-600" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" d="M3.375 19.5h17.25m-17.25 0a1.125 1.125 0 0 1-1.125-1.125M3.375 19.5h7.5c.621 0 1.125-.504 1.125-1.125m-9.75 0V5.625m0 12.75v-1.5c0-.621.504-1.125 1.125-1.125m18.375 2.625V5.625m0 12.75c0 .621-.504 1.125-1.125 1.125m1.125-1.125v-1.5c0-.621-.504-1.125-1.125-1.125m0 3.75h-7.5A1.125 1.125 0 0 1 12 18.375m9.75-12.75c0-.621-.504-1.125-1.125-1.125H3.375c-.621 0-1.125.504-1.125 1.125m19.5 0v1.5c0 .621-.504 1.125-1.125 1.125M2.25 5.625v1.5c0 .621.504 1.125 1.125 1.125m0 0h17.25m-17.25 0h7.5c.621 0 1.125.504 1.125 1.125M3.375 8.25c-.621 0-1.125.504-1.125 1.125v1.5c0 .621.504 1.125 1.125 1.125m17.25-3.75h-7.5c-.621 0-1.125.504-1.125 1.125m8.625-1.125c.621 0 1.125.504 1.125 1.125v1.5c0 .621-.504 1.125-1.125 1.125m-17.25 0h7.5m-7.5 0c-.621 0-1.125.504-1.125 1.125v1.5c0 .621.504 1.125 1.125 1.125M12 10.875v-1.5m0 1.5c0 .621-.504 1.125-1.125 1.125M12 10.875c0 .621.504 1.125 1.125 1.125m-2.25 0c.621 0 1.125.504 1.125 1.125M13.125 12h7.5m-7.5 0c-.621 0-1.125.504-1.125 1.125M20.625 12c.621 0 1.125.504 1.125 1.125v1.5c0 .621-.504 1.125-1.125 1.125m-17.25 0h7.5M12 14.625v-1.5m0 1.5c0 .621-.504 1.125-1.125 1.125M12 14.625c0 .621.504 1.125 1.125 1.125m-2.25 0c.621 0 1.125.504 1.125 1.125m0 0v1.5c0 .621-.504 1.125-1.125 1.125" />
							</svg>
						{:else if stat.icon === 'storage'}
							<svg class="h-5 w-5 text-eurobase-600" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" d="M20.25 6.375c0 2.278-3.694 4.125-8.25 4.125S3.75 8.653 3.75 6.375m16.5 0c0-2.278-3.694-4.125-8.25-4.125S3.75 4.097 3.75 6.375m16.5 0v11.25c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125V6.375m16.5 0v3.75m-16.5-3.75v3.75m16.5 0v3.75C20.25 16.153 16.556 18 12 18s-8.25-1.847-8.25-4.125v-3.75m16.5 0c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125" />
							</svg>
						{:else}
							<svg class="h-5 w-5 text-eurobase-600" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" d="M17.25 6.75 22.5 12l-5.25 5.25m-10.5 0L1.5 12l5.25-5.25m7.5-3-4.5 16.5" />
							</svg>
						{/if}
					</div>
					<div>
						<div class="text-2xl font-bold text-gray-900">{stat.value}</div>
						<div class="text-xs text-gray-500">{stat.label}</div>
					</div>
				</div>
			</div>
		{/each}
	</div>

	<!-- Get Started guide -->
	{#if !guideDismissed}
		<div class="rounded-xl border border-eurobase-200 bg-white mb-6 overflow-hidden">
			<div class="flex items-center justify-between border-b border-eurobase-100 bg-eurobase-50/50 px-5 py-3.5">
				<div class="flex items-center gap-2.5">
					<div class="flex h-7 w-7 items-center justify-center rounded-lg bg-eurobase-600 text-white">
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M15.59 14.37a6 6 0 0 1-5.84 7.38v-4.8m5.84-2.58a14.98 14.98 0 0 0 6.16-12.12A14.98 14.98 0 0 0 9.631 8.41m5.96 5.96a14.926 14.926 0 0 1-5.841 2.58m-.119-8.54a6 6 0 0 0-7.381 5.84h4.8m2.581-5.84a14.927 14.927 0 0 0-2.58 5.84m2.699 2.7c-.103.021-.207.041-.311.06a15.09 15.09 0 0 1-2.448-2.448 14.9 14.9 0 0 1 .06-.312m-2.24 2.39a4.493 4.493 0 0 0-1.757 4.306 4.493 4.493 0 0 0 4.306-1.758M16.5 9a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0Z" />
						</svg>
					</div>
					<h2 class="text-sm font-semibold text-gray-900">Get Started</h2>
				</div>
				<button
					type="button"
					class="cursor-pointer rounded-lg p-1 text-gray-400 hover:bg-gray-100 hover:text-gray-600 transition-colors"
					onclick={() => (guideDismissed = true)}
					title="Dismiss"
				>
					<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12" /></svg>
				</button>
			</div>
			<div class="px-5 py-5 space-y-5">
				{#each getStartedSteps as step, i}
					<div class="flex gap-4">
						<div class="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700">{i + 1}</div>
						<div class="flex-1 min-w-0">
							<h3 class="text-sm font-semibold text-gray-900">{step.title}</h3>
							<p class="mt-0.5 text-xs text-gray-500">{step.desc}</p>
							<div class="relative mt-2">
								<pre class="rounded-lg bg-gray-900 p-3 text-xs font-mono text-green-400 overflow-x-auto">{step.code}</pre>
								<button
									type="button"
									class="cursor-pointer absolute top-2 right-2 rounded-md bg-gray-800 px-2 py-0.5 text-[10px] font-medium text-gray-400 hover:text-white transition-colors"
									onclick={() => copyCode(step.code, step.id)}
								>
									{copiedStep === step.id ? 'Copied!' : 'Copy'}
								</button>
							</div>
						</div>
					</div>
				{/each}
				<div class="flex items-center gap-3 pt-2 border-t border-gray-100">
					<a
						href="/p/{projectId}/connect"
						class="inline-flex items-center gap-1.5 text-xs font-medium text-eurobase-600 hover:text-eurobase-700 transition-colors"
					>
						<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M17.25 6.75 22.5 12l-5.25 5.25m-10.5 0L1.5 12l5.25-5.25m7.5-3-4.5 16.5" />
						</svg>
						IDE setup &amp; config files
					</a>
					<span class="text-gray-300">|</span>
					<a
						href="/p/{projectId}/api"
						class="inline-flex items-center gap-1.5 text-xs font-medium text-eurobase-600 hover:text-eurobase-700 transition-colors"
					>
						<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M12 6.042A8.967 8.967 0 0 0 6 3.75c-1.052 0-2.062.18-3 .512v14.25A8.987 8.987 0 0 1 6 18c2.305 0 4.408.867 6 2.292m0-14.25a8.966 8.966 0 0 1 6-2.292c1.052 0 2.062.18 3 .512v14.25A8.987 8.987 0 0 0 18 18a8.967 8.967 0 0 0-6 2.292m0-14.25v14.25" />
						</svg>
						Full API reference
					</a>
					<span class="text-gray-300">|</span>
					<a
						href="/p/{projectId}/users"
						class="inline-flex items-center gap-1.5 text-xs font-medium text-eurobase-600 hover:text-eurobase-700 transition-colors"
					>
						<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M15.75 6a3.75 3.75 0 1 1-7.5 0 3.75 3.75 0 0 1 7.5 0ZM4.501 20.118a7.5 7.5 0 0 1 14.998 0A17.933 17.933 0 0 1 12 21.75c-2.676 0-5.216-.584-7.499-1.632Z" />
						</svg>
						Auth guide
					</a>
				</div>
			</div>
		</div>
	{/if}

	<!-- Quick actions -->
	<div>
		<h2 class="text-sm font-semibold text-gray-900 mb-3">Quick Actions</h2>
		<div class="flex flex-wrap gap-3">
			<a
				href="/p/{projectId}/database"
				class="inline-flex items-center gap-2 rounded-lg bg-eurobase-600 px-4 py-2.5 text-sm font-medium text-white hover:bg-eurobase-700 transition-colors"
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" d="M20.25 6.375c0 2.278-3.694 4.125-8.25 4.125S3.75 8.653 3.75 6.375m16.5 0c0-2.278-3.694-4.125-8.25-4.125S3.75 4.097 3.75 6.375m16.5 0v11.25c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125V6.375" />
				</svg>
				Open Database
			</a>
			<a
				href="/p/{projectId}/storage"
				class="inline-flex items-center gap-2 rounded-lg bg-emerald-600 px-4 py-2.5 text-sm font-medium text-white hover:bg-emerald-700 transition-colors"
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" d="M2.25 12.75V12A2.25 2.25 0 0 1 4.5 9.75h15A2.25 2.25 0 0 1 21.75 12v.75m-8.69-6.44-2.12-2.12a1.5 1.5 0 0 0-1.061-.44H4.5A2.25 2.25 0 0 0 2.25 6v12a2.25 2.25 0 0 0 2.25 2.25h15A2.25 2.25 0 0 0 21.75 18V9a2.25 2.25 0 0 0-2.25-2.25h-5.379a1.5 1.5 0 0 1-1.06-.44Z" />
				</svg>
				Open Storage
			</a>
			<a
				href="/p/{projectId}/settings"
				class="inline-flex items-center gap-2 rounded-lg bg-violet-600 px-4 py-2.5 text-sm font-medium text-white hover:bg-violet-700 transition-colors"
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" d="M15.75 5.25a3 3 0 0 1 3 3m3 0a6 6 0 0 1-7.029 5.912c-.563-.097-1.159.026-1.563.43L10.5 17.25H8.25v2.25H6v2.25H2.25v-2.818c0-.597.237-1.17.659-1.591l6.499-6.499c.404-.404.527-1 .43-1.563A6 6 0 1 1 21.75 8.25Z" />
				</svg>
				View API Keys
			</a>
		</div>
	</div>
{:else}
	<div class="flex items-center justify-center py-12">
		<div class="flex items-center gap-2 text-sm text-gray-400">
			<svg class="h-5 w-5 animate-spin" viewBox="0 0 24 24" fill="none">
				<circle cx="12" cy="12" r="10" stroke="currentColor" stroke-width="3" class="opacity-25" />
				<path d="M4 12a8 8 0 018-8" stroke="currentColor" stroke-width="3" stroke-linecap="round" class="opacity-75" />
			</svg>
			Loading project...
		</div>
	</div>
{/if}
