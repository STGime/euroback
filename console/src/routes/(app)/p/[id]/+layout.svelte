<script lang="ts">
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { api, type ConnectionState, type Project } from '$lib/api.js';
	import { onDestroy, setContext } from 'svelte';

	let { children } = $props();

	let projectId = $derived($page.params.id);
	let project: Project | null = $state(null);
	let loading = $state(true);
	let error: string | null = $state(null);

	// Dedicated-DB state banner (Team-tier). Poll while
	// provisioning/restoring so a user on ANY tab (not just
	// Database → Connection) sees when the DB is or isn't ready.
	// Free/Pro projects have no project_databases row → 409 with
	// `no_active_dedicated_db` code → we treat that as "no banner
	// needed" (dbState stays null).
	let dbState = $state<ConnectionState | null>(null);
	let dbStatePollTimer: ReturnType<typeof setInterval> | null = null;

	async function refreshDbState() {
		if (!projectId) return;
		try {
			dbState = await api.getConnectionState(projectId);
		} catch {
			// 409 no-dedicated-db, transient network, etc.
			// Leave dbState null → banner stays hidden.
			dbState = null;
		}
		// Keep polling while the DB is coming up; stop once it
		// lands in a terminal state (active / failed) — same
		// cadence + rationale as the Connection tab's own poller.
		if (dbState && (dbState.state === 'provisioning' || dbState.state === 'restoring')) {
			if (!dbStatePollTimer) dbStatePollTimer = setInterval(refreshDbState, 5000);
		} else {
			stopDbStatePoll();
		}
	}
	function stopDbStatePoll() {
		if (dbStatePollTimer) {
			clearInterval(dbStatePollTimer);
			dbStatePollTimer = null;
		}
	}
	onDestroy(stopDbStatePoll);

	// Elapsed-time display for the provisioning banner. Ticker
	// only runs while there IS a banner to show — same shape as
	// the Connection page's own timer.
	let now = $state(Date.now());
	let tickTimer: ReturnType<typeof setInterval> | null = null;
	let elapsedSec = $derived.by(() => {
		if (!dbState?.created_at) return 0;
		const started = new Date(dbState.created_at).getTime();
		return Math.max(0, Math.floor((now - started) / 1000));
	});
	function elapsedLabel(sec: number): string {
		if (sec < 60) return `${sec}s`;
		const m = Math.floor(sec / 60);
		const s = sec % 60;
		return s === 0 ? `${m}m` : `${m}m ${s}s`;
	}
	$effect(() => {
		const active = dbState?.state === 'provisioning' || dbState?.state === 'restoring';
		if (active && !tickTimer) {
			tickTimer = setInterval(() => { now = Date.now(); }, 5000);
		} else if (!active && tickTimer) {
			clearInterval(tickTimer);
			tickTimer = null;
		}
	});
	onDestroy(() => { if (tickTimer) clearInterval(tickTimer); });

	const tabs = [
		{ label: 'Overview', href: '', icon: 'overview' },
		{ label: 'Database', href: '/database', icon: 'database' },
		{ label: 'Storage', href: '/storage', icon: 'storage' },
		{ label: 'Auth', href: '/auth', icon: 'auth' },
		{ label: 'Users', href: '/users', icon: 'users' },
		{ label: 'Logs', href: '/logs', icon: 'logs' },
		{ label: 'API', href: '/api', icon: 'api' },
		{ label: 'Connect', href: '/connect', icon: 'connect' },
		{ label: 'Webhooks', href: '/webhooks', icon: 'webhooks' },
		{ label: 'Vault', href: '/vault', icon: 'vault' },
		{ label: 'Functions', href: '/functions', icon: 'functions' },
		{ label: 'Cron & RPC', href: '/cron', icon: 'cron' },
		{ label: 'Compliance', href: '/compliance', icon: 'compliance' },
		{ label: 'Settings', href: '/settings', icon: 'settings' }
	];

	let copied = $state(false);
	function copyProjectId() {
		navigator.clipboard.writeText(projectId);
		copied = true;
		setTimeout(() => { copied = false; }, 1500);
	}

	let currentTab = $derived(() => {
		const path = $page.url.pathname;
		const base = `/p/${projectId}`;
		const sub = path.replace(base, '');
		return sub || '';
	});

	$effect(() => {
		loadProject();
		// Fetch dedicated-DB state alongside the project. If it comes
		// back provisioning/restoring, refreshDbState arms the poller
		// itself so subpages navigating in also inherit the ticker.
		refreshDbState();
	});

	async function loadProject() {
		loading = true;
		error = null;
		try {
			project = await api.getProject(projectId);
		} catch (err) {
			let msg = err instanceof Error ? err.message : 'Failed to load project';
			if (msg.includes('Project not found')) {
				goto('/projects');
				return;
			}
			if (msg.includes('500') || msg.includes('fetch') || msg.includes('Failed to fetch')) {
				msg = 'Could not connect to the server. Please check that the gateway is running.';
			}
			error = msg;
		} finally {
			loading = false;
		}
	}

	setContext('projectId', {
		get id() { return projectId; },
		get project() { return project; },
		updateProject(p: Project) { project = p; },
		reload: loadProject
	});
</script>

<!-- Project secondary nav -->
<div class="mb-6">
	{#if loading}
		<div class="mb-4 h-7 w-48 animate-pulse rounded bg-gray-200"></div>
	{:else if error}
		<div class="mb-4 flex items-center gap-3 rounded-lg border border-red-200 bg-red-50 px-4 py-3">
			<svg class="h-5 w-5 shrink-0 text-red-500" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
			</svg>
			<span class="text-sm text-red-700">{error}</span>
			<button
				onclick={loadProject}
				class="ml-auto shrink-0 rounded-md bg-red-100 px-3 py-1 text-xs font-medium text-red-700 hover:bg-red-200 transition-colors cursor-pointer"
			>Retry</button>
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
			<button
				type="button"
				onclick={copyProjectId}
				class="cursor-pointer inline-flex items-center gap-1.5 rounded-md bg-gray-100 px-2 py-1 text-[11px] text-gray-500 hover:bg-gray-200 hover:text-gray-700 transition-colors"
				title="Copy project ID"
			>
				<span class="font-medium text-gray-400">Project ID</span>
				<span class="font-mono">{projectId}</span>
				{#if copied}
					<svg class="h-3.5 w-3.5 text-green-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M4.5 12.75l6 6 9-13.5" />
					</svg>
				{:else}
					<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M15.666 3.888A2.25 2.25 0 0 0 13.5 2.25h-3c-1.03 0-1.9.693-2.166 1.638m7.332 0c.055.194.084.4.084.612v0a.75.75 0 0 1-.75.75H9.75a.75.75 0 0 1-.75-.75v0c0-.212.03-.418.084-.612m7.332 0c.646.049 1.288.11 1.927.184 1.1.128 1.907 1.077 1.907 2.185V19.5a2.25 2.25 0 0 1-2.25 2.25H6.75A2.25 2.25 0 0 1 4.5 19.5V6.257c0-1.108.806-2.057 1.907-2.185a48.208 48.208 0 0 1 1.927-.184" />
					</svg>
				{/if}
			</button>
		</div>
	{/if}

	<nav class="flex gap-1 border-b border-gray-200">
		{#each tabs as tab}
			{@const sub = currentTab()}
			{@const isActive = tab.href === '' ? sub === '' : sub.startsWith(tab.href)}
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

<!-- Dedicated-DB status banner. Shown across every project subpage
     while the Team-tier Scaleway instance isn't ready yet — the
     Connection tab has its own detailed view, this is the ambient
     awareness so a user doesn't create tables / run SQL / hit the
     SDK while the backing DB is still spinning up. Free/Pro
     projects have dbState=null and render nothing. -->
{#if dbState}
	{#if dbState.state === 'provisioning'}
		<div class="mb-4 flex items-center gap-3 rounded-md border border-eurobase-200 bg-eurobase-50 px-4 py-3 text-sm text-eurobase-900">
			<svg class="h-5 w-5 shrink-0 animate-spin text-eurobase-600" fill="none" viewBox="0 0 24 24">
				<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
				<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
			</svg>
			<div class="min-w-0 flex-1">
				<p class="font-semibold">Provisioning your dedicated Postgres…</p>
				<p class="mt-0.5 text-xs text-eurobase-800">
					Scaleway takes 2–5 minutes. Elapsed: <span class="font-mono">{elapsedLabel(elapsedSec)}</span>.
					Tables, SQL, and SDK writes for this project will fail until it's ready.
				</p>
			</div>
			<a href="/p/{projectId}/database/connection" class="shrink-0 text-xs font-medium text-eurobase-700 underline hover:text-eurobase-800">Details</a>
		</div>
	{:else if dbState.state === 'restoring'}
		<div class="mb-4 flex items-center gap-3 rounded-md border border-blue-200 bg-blue-50 px-4 py-3 text-sm text-blue-900">
			<svg class="h-5 w-5 shrink-0 animate-spin text-blue-600" fill="none" viewBox="0 0 24 24">
				<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
				<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
			</svg>
			<div class="min-w-0 flex-1">
				<p class="font-semibold">Restore in progress</p>
				<p class="mt-0.5 text-xs">The dedicated instance is restoring from a snapshot. Elapsed: <span class="font-mono">{elapsedLabel(elapsedSec)}</span>.</p>
			</div>
			<a href="/p/{projectId}/database/connection" class="shrink-0 text-xs font-medium text-blue-700 underline hover:text-blue-800">Details</a>
		</div>
	{:else if dbState.state === 'failed' || dbState.retryable}
		<!-- retryable=true also covers "no project_databases row at all"
		     (enqueue failed at CreateProject) — same user affordance:
		     go to Connection and click Retry. -->
		<div class="mb-4 flex items-center gap-3 rounded-md border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-900">
			<svg class="h-5 w-5 shrink-0 text-red-500" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
			</svg>
			<div class="min-w-0 flex-1">
				<p class="font-semibold">{dbState.state === 'failed' ? 'Dedicated Postgres provisioning failed' : 'Dedicated Postgres never provisioned'}</p>
				<p class="mt-0.5 text-xs">Tables / SQL / SDK writes for this project will keep failing until the instance is up.</p>
			</div>
			<a href="/p/{projectId}/database/connection" class="shrink-0 rounded-md bg-red-100 px-3 py-1 text-xs font-medium text-red-700 hover:bg-red-200">Fix</a>
		</div>
	{/if}
{/if}

{#key $page.url.pathname}
<div>
	{@render children()}
</div>
{/key}
