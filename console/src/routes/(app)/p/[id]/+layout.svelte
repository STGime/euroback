<script lang="ts">
	import { page } from '$app/stores';
	import { api, type Project } from '$lib/api.js';
	import { setContext } from 'svelte';

	let { children } = $props();

	let projectId = $derived($page.params.id);
	let project: Project | null = $state(null);
	let loading = $state(true);
	let error: string | null = $state(null);

	const tabs = [
		{ label: 'Overview', href: '', icon: 'overview' },
		{ label: 'Database', href: '/database', icon: 'database' },
		{ label: 'Storage', href: '/storage', icon: 'storage' },
		{ label: 'API', href: '/api', icon: 'api' },
		{ label: 'Settings', href: '/settings', icon: 'settings' }
	];

	let currentTab = $derived(() => {
		const path = $page.url.pathname;
		const base = `/p/${projectId}`;
		const sub = path.replace(base, '');
		return sub || '';
	});

	$effect(() => {
		loadProject();
	});

	async function loadProject() {
		loading = true;
		error = null;
		try {
			project = await api.getProject(projectId);
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load project';
		} finally {
			loading = false;
		}
	}

	setContext('projectId', {
		get id() { return projectId; },
		get project() { return project; }
	});
</script>

<!-- Project secondary nav -->
<div class="mb-6">
	{#if loading}
		<div class="mb-4 h-7 w-48 animate-pulse rounded bg-gray-200"></div>
	{:else if error}
		<div class="mb-4 rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-600">
			{error}
		</div>
	{:else if project}
		<div class="mb-4 flex items-center gap-3">
			<a href="/projects" class="text-gray-400 hover:text-gray-600 transition-colors" aria-label="Back to projects">
				<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" d="M15.75 19.5 8.25 12l7.5-7.5" />
				</svg>
			</a>
			<h1 class="text-xl font-bold text-gray-900">{project.name}</h1>
			<span class="inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium
				{project.status === 'active' ? 'bg-green-100 text-green-700' :
				 project.status === 'provisioning' ? 'bg-amber-100 text-amber-700' :
				 'bg-gray-100 text-gray-600'}">
				{project.status}
			</span>
		</div>
	{/if}

	<nav class="flex gap-1 border-b border-gray-200">
		{#each tabs as tab}
			{@const isActive = currentTab() === tab.href}
			<a
				href="/p/{projectId}{tab.href}"
				class="relative px-4 py-2.5 text-sm font-medium transition-colors
					{isActive
						? 'text-eurobase-700'
						: 'text-gray-500 hover:text-gray-700'}"
			>
				{tab.label}
				{#if isActive}
					<span class="absolute bottom-0 left-0 right-0 h-0.5 bg-eurobase-600 rounded-full"></span>
				{/if}
			</a>
		{/each}
	</nav>
</div>

<div>
	{@render children()}
</div>
