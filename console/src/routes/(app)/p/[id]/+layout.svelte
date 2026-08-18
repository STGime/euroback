<script lang="ts">
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { api, APIError, type ConnectionState, type Project } from '$lib/api.js';
	import { onDestroy, setContext } from 'svelte';

	let { children } = $props();

	let projectId = $derived($page.params.id);
	let project: Project | null = $state(null);
	let loading = $state(true);
	let error: string | null = $state(null);

	// Dedicated-DB state banner (Team-tier). Poll while the DB
	// isn't `active` so a user on ANY tab (not just Database →
	// Connection) sees ambient status.
	//
	// Free/Pro projects: **skip the poll entirely** rather than
	// hitting the endpoint and swallowing the 402. The prior shape
	// fired a request, caught the `dedicated_db_required` 402, and
	// stopped — correct behavior but the request still landed in
	// every project's audit log, looking like a real error to
	// customers who happened to look. A Bertram support email
	// (2026-08-18) mistook these 402s for a real bug affecting his
	// app. The 402-catch below stays as a belt-and-suspenders
	// (project.plan reads as "" during the load window, or a plan
	// column gets renamed, etc.), but the primary control is now
	// "don't ask a question whose answer is always the same."
	//
	// Team-tier (dedicated_db=true per plan_limits) is currently
	// only 'team' and 'legal_team'; the check tracks those two
	// values explicitly rather than pulling the flag from
	// plan_limits so a fresh page load doesn't need to await a
	// second network round-trip before it can start polling.
	//
	// state === '' is the "row not yet inserted" case: CreateProject
	// commits + enqueues the worker job BEFORE the worker's
	// InsertProvisioning call lands the project_databases row. That
	// window is normally seconds but can stretch to minutes under
	// River retry backoff. Treat it as "provisioning, not yet
	// recorded" — render the neutral setup banner AND keep polling
	// so the state transitions get picked up. A previous version
	// routed '' + retryable=true to the RED "never provisioned"
	// affordance which latched permanently (no poll re-arm) and was
	// wrong on the fresh-project happy path.
	let dbState = $state<ConnectionState | null>(null);
	let dbStatePollTimer: ReturnType<typeof setInterval> | null = null;

	// Consecutive `state === ''` ticks. The empty state is normal
	// for a few seconds while CreateProject's enqueue lands the
	// project_databases row, but the SAME empty response signals a
	// genuine enqueue failure (retryable=true, no row ever
	// scheduled). If we keep seeing it past River's retry window
	// we escalate to the red "provisioning never started" banner
	// to match what the Connection tab shows for the same case.
	// 5s × 15 = 75s window — comfortably past River's default
	// backoff for the provision job's first retries.
	let emptyStateTicks = $state(0);
	const EMPTY_STATE_ESCALATE_AFTER = 15;

	function isTransientState(s: ConnectionState | null): boolean {
		if (!s) return false;
		return s.state === '' || s.state === 'provisioning' || s.state === 'restoring';
	}

	// Only Team-tier and above have a dedicated DB → only they need
	// /connection/state polled. Returns false while `project` is
	// still loading (nil) so we don't fire a speculative request; the
	// dedicated $effect below re-triggers refreshDbState the moment
	// project resolves and passes the check.
	function planUsesConnectionState(p: Project | null): boolean {
		if (!p) return false;
		return p.plan === 'team' || p.plan === 'legal_team';
	}

	function armDbStatePoll() {
		if (!dbStatePollTimer) dbStatePollTimer = setInterval(refreshDbState, 5000);
	}

	async function refreshDbState() {
		if (!projectId) return;
		// Primary guard: don't call the endpoint for tiers that
		// don't have a dedicated DB. If project is still loading,
		// bail — the follow-up $effect will re-fire once project
		// resolves.
		if (!planUsesConnectionState(project)) {
			dbState = null;
			stopDbStatePoll();
			return;
		}
		try {
			dbState = await api.getConnectionState(projectId);
			// Track how long the empty-row window has been open so
			// isEnqueueStuck() can escalate the banner. Reset on any
			// non-empty response so a state that flips back to
			// provisioning (unlikely but possible) doesn't stick.
			if (dbState.state === '') emptyStateTicks++;
			else emptyStateTicks = 0;
		} catch (err) {
			if (err instanceof APIError && err.status === 402) {
				// Free/Pro — no dedicated instance by design.
				// Definitive answer; stop polling.
				dbState = null;
				stopDbStatePoll();
				return;
			}
			// Transient (network / 500 / auth blip). Keep the last
			// known dbState so the banner doesn't vanish mid-
			// provisioning, and ARM the poll so a blip on the FIRST
			// fetch doesn't latch silence forever (no other event
			// re-arms except a projectId change).
			armDbStatePoll();
			return;
		}
		if (isTransientState(dbState)) {
			armDbStatePoll();
		} else {
			stopDbStatePoll();
		}
	}

	// isEnqueueStuck escalates state==='' to a real failure once
	// we've polled past the "normal enqueue delay" window. Only
	// meaningful when the backend also reports retryable=true —
	// i.e. it's the "no row exists" branch, not some new transient
	// state we haven't handled.
	let isEnqueueStuck = $derived(
		dbState?.state === '' &&
		dbState.retryable === true &&
		emptyStateTicks >= EMPTY_STATE_ESCALATE_AFTER
	);
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
		// Only tick when there's a real created_at to count from
		// (provisioning/restoring rows have one; state==='' doesn't).
		const needsTicker = dbState?.state === 'provisioning' || dbState?.state === 'restoring';
		if (needsTicker && !tickTimer) {
			tickTimer = setInterval(() => { now = Date.now(); }, 5000);
		} else if (!needsTicker && tickTimer) {
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
		{ label: 'Billing', href: '/billing', icon: 'billing' },
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
		// projectId is a reactive read — this effect re-runs on
		// every project switch. SvelteKit reuses the layout
		// component across /p/A → /p/B (same route), so anything
		// derived from the old project's state must be reset here
		// or it poisons the new project. Concretely: without the
		// reset, `emptyStateTicks` from a stuck project A carries
		// over and the very first render of a fresh project B
		// shows the RED "never provisioned" banner — the same
		// false-positive the earlier rounds were fixing, reachable
		// by "look at a broken project for 75s, then open a new
		// one." dbState also gets nulled so a provisioning → active
		// transition across project switches doesn't render stale.
		void projectId; // depend on projectId explicitly
		dbState = null;
		emptyStateTicks = 0;
		stopDbStatePoll();
		loadProject();
		// refreshDbState is fired by the plan-watch $effect below
		// once `project` resolves, so we don't fire a speculative
		// pre-load call here — that was the source of the
		// per-Free-project 402 in every user's audit log.
	});

	// Fire the dedicated-DB refresh only after `project` loads and
	// only for tiers that have one. Re-runs on plan change (rare —
	// upgrade flow) so an upgrade to Team starts the poll without
	// a page reload.
	$effect(() => {
		void project?.plan; // reactive dep
		if (planUsesConnectionState(project)) {
			refreshDbState();
		}
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
     projects have dbState=null and render nothing.

     role=status + aria-live=polite so screen readers announce the
     banner when it appears mid-session; decorative SVGs are
     aria-hidden. -->
{#if dbState}
	{#if dbState.state === '' && !isEnqueueStuck}
		<!-- Row not yet inserted — CreateProject just committed +
		     enqueued the worker; refresh has been faster than the
		     worker's InsertProvisioning. Neutral banner + keep
		     polling. Escalates to the red "never provisioned"
		     variant below if this drags past EMPTY_STATE_ESCALATE_AFTER
		     ticks (~75s), matching what the Connection tab shows for
		     the same case. -->
		<div role="status" aria-live="polite" class="mb-4 flex items-center gap-3 rounded-md border border-eurobase-200 bg-eurobase-50 px-4 py-3 text-sm text-eurobase-900">
			<svg aria-hidden="true" class="h-5 w-5 shrink-0 animate-spin text-eurobase-600" fill="none" viewBox="0 0 24 24">
				<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
				<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
			</svg>
			<div class="min-w-0 flex-1">
				<p class="font-semibold">Setting up your dedicated Postgres…</p>
				<p class="mt-0.5 text-xs text-eurobase-800">Waiting for the provisioning job to record the instance. This normally takes a few seconds.</p>
			</div>
			<a href="/p/{projectId}/database/connection" class="shrink-0 text-xs font-medium text-eurobase-700 underline hover:text-eurobase-800">Details</a>
		</div>
	{:else if isEnqueueStuck}
		<!-- state==='' for longer than River's normal retry window
		     — the enqueue itself failed at CreateProject time and
		     no row will ever land without a manual retry. Same
		     surface the Connection tab shows for this case. -->
		<div role="alert" aria-live="assertive" class="mb-4 flex items-center gap-3 rounded-md border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-900">
			<svg aria-hidden="true" class="h-5 w-5 shrink-0 text-red-500" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
			</svg>
			<div class="min-w-0 flex-1">
				<p class="font-semibold">Dedicated Postgres was never provisioned</p>
				<p class="mt-0.5 text-xs">The provisioning job never landed a project_databases row. Tables / SQL / SDK writes will keep failing until Retry starts a fresh job.</p>
			</div>
			<a href="/p/{projectId}/database/connection" class="shrink-0 rounded-md bg-red-100 px-3 py-1 text-xs font-medium text-red-700 hover:bg-red-200">Fix</a>
		</div>
	{:else if dbState.state === 'provisioning'}
		<div role="status" aria-live="polite" class="mb-4 flex items-center gap-3 rounded-md border border-eurobase-200 bg-eurobase-50 px-4 py-3 text-sm text-eurobase-900">
			<svg aria-hidden="true" class="h-5 w-5 shrink-0 animate-spin text-eurobase-600" fill="none" viewBox="0 0 24 24">
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
		<div role="status" aria-live="polite" class="mb-4 flex items-center gap-3 rounded-md border border-blue-200 bg-blue-50 px-4 py-3 text-sm text-blue-900">
			<svg aria-hidden="true" class="h-5 w-5 shrink-0 animate-spin text-blue-600" fill="none" viewBox="0 0 24 24">
				<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
				<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
			</svg>
			<div class="min-w-0 flex-1">
				<p class="font-semibold">Restore in progress</p>
				<p class="mt-0.5 text-xs">The dedicated instance is restoring from a snapshot. Elapsed: <span class="font-mono">{elapsedLabel(elapsedSec)}</span>.</p>
			</div>
			<a href="/p/{projectId}/database/connection" class="shrink-0 text-xs font-medium text-blue-700 underline hover:text-blue-800">Details</a>
		</div>
	{:else if dbState.state === 'failed'}
		<!-- Terminal failure — the ONLY case that should route to the
		     red "Fix" call-to-action. state==='' also carries
		     retryable=true from the backend but that's the fresh-
		     project window handled above. -->
		<div role="alert" aria-live="assertive" class="mb-4 flex items-center gap-3 rounded-md border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-900">
			<svg aria-hidden="true" class="h-5 w-5 shrink-0 text-red-500" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
			</svg>
			<div class="min-w-0 flex-1">
				<p class="font-semibold">Dedicated Postgres provisioning failed</p>
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
