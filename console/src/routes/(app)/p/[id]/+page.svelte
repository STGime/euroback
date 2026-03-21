<script lang="ts">
	import { page } from '$app/stores';
	import { getContext } from 'svelte';

	let projectId = $derived($page.params.id);
	let ctx: { project: import('$lib/api.js').Project | null } = getContext('projectId');
	let project = $derived(ctx.project);

	const stats = [
		{ label: 'Tables', value: '—', icon: 'table' },
		{ label: 'Storage used', value: '—', icon: 'storage' },
		{ label: 'API requests today', value: '—', icon: 'api' }
	];

	let quickActions = $derived([
		{ label: 'Open Database', href: `/p/${projectId}/database`, color: 'eurobase' },
		{ label: 'Open Storage', href: `/p/${projectId}/storage`, color: 'emerald' },
		{ label: 'View API Keys', href: `/p/${projectId}/settings`, color: 'violet' }
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
