<script lang="ts">
	import { page } from '$app/stores';
	import { onMount, onDestroy } from 'svelte';
	import { api, APIError, type ConnectionInfo, type ConnectionState, type Project } from '$lib/api.js';

	let projectId = $derived($page.params.id);
	let project = $state<Project | null>(null);
	let conn = $state<ConnectionInfo | null>(null);
	// `role` is what the user *requested* — the effective role the URL
	// grants is `conn.role` (backend truth). During the readonly_pending
	// window they can differ.
	let role = $state<'readonly' | 'readwrite'>('readonly');

	let revealed = $state(false);
	let error = $state<string | null>(null);
	let busy = $state(false);

	// Rotate confirm modal
	let rotateConfirmOpen = $state(false);
	let rotateBusy = $state(false);
	let rotateError = $state<string | null>(null);
	let typedForRotate = $state('');
	let rotateMatches = $derived(typedForRotate.trim() === (project?.name ?? ''));

	// State-poll surface. Split from `error` so a transient state
	// lookup (network blip on the 5s poll) doesn't wipe the connection
	// UX out from under a working session — `error` is reserved for
	// the once-per-load /connection call.
	let dbState = $state<ConnectionState | null>(null);
	let stateError = $state<string | null>(null);
	let retryBusy = $state(false);
	let retryError = $state<string | null>(null);

	// 5s poll cadence — long enough that Scaleway RDB provisioning
	// (2-5 min typical) doesn't hammer the state endpoint, short
	// enough that the user sees the ready state within a few seconds
	// of it landing. Poll only while the DB is provisioning/restoring
	// (no point polling once active — the URL is stable) and only
	// while the tab is open (onDestroy clears the interval so a
	// navigation away doesn't leave a stray timer).
	let pollTimer: ReturnType<typeof setInterval> | null = null;
	function stopPoll() {
		if (pollTimer !== null) {
			clearInterval(pollTimer);
			pollTimer = null;
		}
	}
	function startPollIfNeeded() {
		if (dbState && (dbState.state === 'provisioning' || dbState.state === 'restoring')) {
			if (pollTimer === null) pollTimer = setInterval(refreshState, 5000);
		} else {
			stopPoll();
		}
	}

	// Elapsed-time display for the provisioning banner. Derived from
	// dbState.created_at + a $state ticker that updates every 5s so
	// the number moves without the whole page re-rendering constantly.
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

	async function refreshState() {
		if (!projectId) return;
		try {
			const s = await api.getConnectionState(projectId);
			const wasActive = dbState?.state === 'active';
			dbState = s;
			stateError = null;
			// State transitioned INTO active — load the URL. Load the
			// project too if it isn't cached yet so the rotate-confirm
			// modal has the name for its typed-confirmation guard.
			if (s.state === 'active' && !conn) {
				await load(role);
			}
			// State transitioned OUT of active back to something else
			// (e.g. rotation ripped the URL out of sight) → drop cached
			// conn so we re-fetch on the next transition.
			if (s.state !== 'active' && wasActive) {
				conn = null;
			}
			startPollIfNeeded();
		} catch (e: any) {
			stateError = e?.message ?? 'State lookup failed';
		}
	}

	async function load(withRole: 'readonly' | 'readwrite') {
		if (!projectId) return;
		busy = true;
		error = null;
		try {
			const [p, c] = await Promise.all([
				project ? Promise.resolve(project) : api.getProject(projectId),
				api.getConnection(projectId, withRole)
			]);
			project = p;
			conn = c;
			role = withRole;
		} catch (e: any) {
			// Suppress the top-of-page red banner for the
			// no-active-DB 409 — the state banner already renders
			// the "provisioning" / "failed" context. Keyed on the
			// machine-readable `code` (via APIError) rather than
			// substring-matching the message, so a backend reword
			// can't re-open the double-surface bug.
			const code = e instanceof APIError ? e.code : undefined;
			if (code !== 'no_active_dedicated_db') {
				error = e?.message ?? 'Failed to load connection';
			}
			conn = null;
		} finally {
			busy = false;
		}
	}

	async function retryProvision() {
		if (!projectId) return;
		retryBusy = true;
		retryError = null;
		try {
			await api.retryProvisionConnection(projectId);
			// Optimistically enter provisioning + start polling.
			//
			// The retry handler MarkDeleted's the old failed row
			// (state='deleting') BEFORE the new River job runs
			// InsertProvisioning. If we `refreshState()` immediately
			// here, we catch that transient 'deleting' row and the
			// template routes to the gray "torn down — create a
			// new project" banner — the exact opposite message
			// from "you just clicked Retry, we're provisioning."
			// Worse: startPollIfNeeded only polls
			// provisioning/restoring, so 'deleting' stops the poll
			// and the UI never advances when the real
			// provisioning row lands.
			//
			// Fix: skip the racy refresh and set the local state
			// optimistically. The next poll tick (5s) picks up the
			// real row and replaces this stub. If the enqueue
			// somehow never materialises a row, the poll keeps
			// showing the spinner — which is the right failure
			// shape (a genuinely stuck provisioning).
			dbState = {
				state: 'provisioning',
				created_at: new Date().toISOString(),
				retryable: false,
			};
			startPollIfNeeded();
		} catch (e: any) {
			retryError = e?.message ?? 'Retry failed';
		} finally {
			retryBusy = false;
		}
	}

	onMount(async () => {
		// Load the project name in parallel with the first state check
		// so the rotate-confirm modal's typed-confirmation guard has
		// what it needs before the user could possibly click.
		if (projectId) {
			try {
				project = await api.getProject(projectId);
			} catch {
				// Non-fatal — modal falls back to a generic prompt.
			}
		}
		await refreshState();
		tickTimer = setInterval(() => { now = Date.now(); }, 5000);
	});

	onDestroy(() => {
		stopPoll();
		if (tickTimer !== null) clearInterval(tickTimer);
	});

	async function copy(text: string) {
		try {
			await navigator.clipboard.writeText(text);
		} catch {
			// best-effort
		}
	}

	async function confirmRotate() {
		if (!projectId) return;
		rotateBusy = true;
		rotateError = null;
		try {
			const c = await api.rotateConnection(projectId);
			conn = c;
			role = 'readwrite';
			revealed = true; // show freshly rotated URL immediately
			rotateConfirmOpen = false;
			typedForRotate = '';
		} catch (e: any) {
			rotateError = e?.message ?? 'Rotate failed';
		} finally {
			rotateBusy = false;
		}
	}
</script>

<div class="space-y-6">
	<header>
		<h1 class="text-lg font-semibold text-gray-900">Direct connection</h1>
		<p class="mt-1 text-xs text-gray-500">
			Your project's dedicated Postgres instance is directly accessible.
			Use the URL with Payload, Prisma, Drizzle, Directus, or <code class="rounded bg-gray-100 px-1 text-[11px]">psql</code>.
		</p>
	</header>

	{#if error}
		<div class="rounded-md bg-red-50 border border-red-200 p-3 text-sm text-red-800">{error}</div>
	{/if}

	{#if dbState && dbState.state === 'provisioning'}
		<!-- Provisioning banner: shown while Scaleway RDB spins up.
		     Typical window is 2-5 min; if elapsed passes 10 min the
		     worker's PollTimeout kicks in and the state flips to
		     'failed', which switches this banner over to the failure
		     one below. -->
		<div class="rounded-md border border-eurobase-200 bg-eurobase-50 p-4 text-sm text-eurobase-900">
			<div class="flex items-center gap-3">
				<svg class="h-5 w-5 animate-spin text-eurobase-600 shrink-0" fill="none" viewBox="0 0 24 24">
					<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
					<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
				</svg>
				<div>
					<p class="font-semibold">Provisioning your dedicated Postgres…</p>
					<p class="mt-0.5 text-xs text-eurobase-800">
						Scaleway takes 2–5 minutes to bring up a managed instance. Elapsed: <span class="font-mono">{elapsedLabel(elapsedSec)}</span>.
						This page will update automatically when it's ready — no need to refresh.
					</p>
				</div>
			</div>
		</div>
	{:else if dbState && dbState.state === 'restoring'}
		<div class="rounded-md border border-blue-200 bg-blue-50 p-4 text-sm text-blue-900">
			<p class="font-semibold">Restore in progress</p>
			<p class="mt-0.5 text-xs">The dedicated instance is restoring from a snapshot. Connection URL will reappear once the restore completes.</p>
		</div>
	{:else if dbState && dbState.retryable}
		<!-- Retryable = state='failed' or no row at all (rare —
		     enqueue failed at CreateProject time). Both need the same
		     user affordance: "click Retry to spin up a fresh Scaleway
		     instance." -->
		<div class="rounded-md border border-red-200 bg-red-50 p-4 text-sm text-red-900">
			<p class="font-semibold">
				{dbState.state === 'failed' ? 'Provisioning failed' : 'Provisioning was never started'}
			</p>
			<p class="mt-0.5 text-xs">
				{#if dbState.state === 'failed'}
					The Scaleway managed-PG provisioning didn't complete. Common causes: invalid Scaleway credentials in the worker, the region being at capacity, or a policy rejection at instance-create time. Click <strong>Retry</strong> to enqueue a fresh attempt; the previous failed row is cleaned up server-side.
				{:else}
					No provisioning job ever ran for this project (rare — usually means the enqueue failed at project-create time). Click <strong>Retry</strong> to start one now.
				{/if}
			</p>
			{#if retryError}
				<p class="mt-2 text-xs font-medium text-red-800">{retryError}</p>
			{/if}
			<div class="mt-3">
				<button
					type="button"
					onclick={retryProvision}
					disabled={retryBusy}
					class="cursor-pointer rounded-md border border-red-300 bg-white px-3 py-1.5 text-xs font-medium text-red-700 hover:bg-red-100 disabled:opacity-50 disabled:cursor-not-allowed"
				>
					{retryBusy ? 'Enqueuing…' : 'Retry provisioning'}
				</button>
			</div>
		</div>
	{:else if dbState && (dbState.state === 'deleting' || dbState.state === 'deleted')}
		<div class="rounded-md border border-gray-200 bg-gray-50 p-4 text-sm text-gray-700">
			<p class="font-semibold">Dedicated database being torn down</p>
			<p class="mt-0.5 text-xs">This project's dedicated instance is deleting. Create a new project on the Team plan to get a fresh one.</p>
		</div>
	{/if}

	{#if dbState && dbState.state === 'active'}
	<div class="rounded-lg border border-gray-200 bg-white p-6 shadow-sm space-y-4">
		<div class="flex items-center justify-between">
			<div class="flex gap-2">
				<button
					type="button"
					onclick={() => load('readonly')}
					disabled={busy}
					class="rounded-md border px-3 py-1.5 text-xs font-medium cursor-pointer transition-colors {role === 'readonly' ? 'border-eurobase-600 bg-eurobase-50 text-eurobase-700' : 'border-gray-300 bg-white text-gray-600 hover:bg-gray-50'}"
				>
					Read-only
				</button>
				<button
					type="button"
					onclick={() => load('readwrite')}
					disabled={busy}
					class="rounded-md border px-3 py-1.5 text-xs font-medium cursor-pointer transition-colors {role === 'readwrite' ? 'border-red-600 bg-red-50 text-red-700' : 'border-gray-300 bg-white text-gray-600 hover:bg-gray-50'}"
				>
					Read/Write
				</button>
			</div>
			<button
				type="button"
				onclick={() => {
					rotateError = null;
					typedForRotate = '';
					rotateConfirmOpen = true;
				}}
				class="text-xs text-red-600 hover:text-red-800 cursor-pointer"
			>
				Rotate password
			</button>
		</div>

		{#if conn}
			<div>
				{#if conn.readonly_pending}
					<!-- The dedicated bootstrap hasn't populated the readonly
					     credential yet (transient — the backfill sweeper or
					     a fresh provisioning pass typically fills it in
					     minutes). The backend would fall back to the owner
					     DSN with `readonly_pending: true`, but rendering it
					     here — even next to a banner — would let a user
					     copy owner creds thinking they're readonly. Hide
					     the URL/Reveal/Copy affordance entirely; keep the
					     Read/Write toggle so the user can request the
					     read/write URL explicitly. -->
					<div class="rounded-md border border-amber-200 bg-amber-50 p-4 text-sm text-amber-900">
						<p class="font-semibold">Read-only role still provisioning</p>
						<p class="mt-1 text-xs">
							This project's dedicated instance is being bootstrapped with a SELECT-only
							role. That normally completes within a couple of minutes of a fresh
							provisioning — refresh this page to check. If you need a URL right now,
							switch to <strong>Read/Write</strong> above; treat that DSN as an owner
							credential (destructive queries allowed).
						</p>
					</div>
				{:else}
					<label for="conn-url" class="block text-xs font-medium text-gray-500 mb-1">Connection URL</label>
					<div class="flex gap-2">
						<input
							id="conn-url"
							type={revealed ? 'text' : 'password'}
							value={conn.url}
							readonly
							class="flex-1 rounded-md border border-gray-300 px-3 py-2 text-sm font-mono text-gray-800"
						/>
						<button
							type="button"
							onclick={() => (revealed = !revealed)}
							class="rounded-md border border-gray-300 bg-white px-3 py-2 text-xs cursor-pointer hover:bg-gray-50"
						>
							{revealed ? 'Hide' : 'Reveal'}
						</button>
						<button
							type="button"
							onclick={() => copy(conn?.url ?? '')}
							class="rounded-md bg-eurobase-600 px-3 py-2 text-xs font-medium text-white cursor-pointer hover:bg-eurobase-700"
						>
							Copy
						</button>
					</div>
					<p class="mt-2 text-xs text-gray-500">
						Every URL fetch is audited — the URL is a bearer credential once revealed.
						{#if conn.role === 'readwrite'}
							<span class="text-red-600">Read/Write access allows destructive queries.</span>
						{/if}
					</p>
				{/if}
			</div>

			<dl class="grid grid-cols-2 gap-3 text-sm border-t border-gray-100 pt-4">
				<div>
					<dt class="text-xs font-medium text-gray-500">Host</dt>
					<dd class="mt-0.5 font-mono text-gray-800">{conn.host}</dd>
				</div>
				<div>
					<dt class="text-xs font-medium text-gray-500">Port</dt>
					<dd class="mt-0.5 font-mono text-gray-800">{conn.port}</dd>
				</div>
				<div>
					<dt class="text-xs font-medium text-gray-500">Database</dt>
					<dd class="mt-0.5 font-mono text-gray-800">{conn.database}</dd>
				</div>
				<div>
					<dt class="text-xs font-medium text-gray-500">Username</dt>
					<dd class="mt-0.5 font-mono text-gray-800">{conn.username}</dd>
				</div>
			</dl>
		{/if}
	</div>
	{/if}

	<div class="rounded-lg border border-gray-200 bg-white p-6 shadow-sm space-y-4">
		<h2 class="text-sm font-semibold text-gray-900">Client examples</h2>

		<div>
			<p class="text-xs font-medium text-gray-500 mb-1">Payload CMS</p>
			<pre class="rounded-md bg-gray-900 p-3 text-xs text-gray-100 overflow-x-auto"><code>// payload.config.ts
import {'{'} postgresAdapter {'}'} from '@payloadcms/db-postgres';

export default buildConfig({'{'}
  db: postgresAdapter({'{'}
    pool: {'{'} connectionString: process.env.DATABASE_URL {'}'},
  {'}'}),
{'}'});</code></pre>
		</div>

		<div>
			<p class="text-xs font-medium text-gray-500 mb-1">Prisma</p>
			<pre class="rounded-md bg-gray-900 p-3 text-xs text-gray-100 overflow-x-auto"><code>// schema.prisma
datasource db {'{'}
  provider = "postgresql"
  url      = env("DATABASE_URL")
{'}'}</code></pre>
		</div>

		<div>
			<p class="text-xs font-medium text-gray-500 mb-1">Drizzle</p>
			<pre class="rounded-md bg-gray-900 p-3 text-xs text-gray-100 overflow-x-auto"><code>import {'{'} drizzle {'}'} from 'drizzle-orm/postgres-js';
import postgres from 'postgres';

const client = postgres(process.env.DATABASE_URL!);
export const db = drizzle(client);</code></pre>
		</div>

		<div>
			<p class="text-xs font-medium text-gray-500 mb-1">psql</p>
			<pre class="rounded-md bg-gray-900 p-3 text-xs text-gray-100 overflow-x-auto"><code>psql "$DATABASE_URL"</code></pre>
		</div>
	</div>
</div>

{#if rotateConfirmOpen}
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<button
			type="button"
			class="fixed inset-0 bg-black/50 cursor-default"
			onclick={() => (rotateConfirmOpen = false)}
			tabindex="-1"
			aria-label="Close"
		></button>
		<div class="relative z-10 w-full max-w-md rounded-xl bg-white shadow-2xl p-6">
			<div class="mb-4">
				<h3 class="text-sm font-semibold text-gray-900">Rotate connection password</h3>
				<p class="mt-1 text-xs text-gray-500">
					The current URL will stop working within seconds. Update every deployed client
					(Payload, Prisma, cron jobs, CI) with the new URL before rotating in production.
				</p>
			</div>

			{#if rotateError}
				<div class="mb-4 rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
					{rotateError}
				</div>
			{/if}

			<div class="mb-5">
				<label for="rotate-confirm" class="block text-sm font-medium text-gray-700 mb-1">
					Type <strong>{project?.name}</strong> to confirm
				</label>
				<input
					id="rotate-confirm"
					type="text"
					bind:value={typedForRotate}
					placeholder={project?.name}
					class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm focus:border-red-500 focus:outline-none"
				/>
			</div>

			<div class="flex justify-end gap-3">
				<button
					type="button"
					onclick={() => (rotateConfirmOpen = false)}
					class="cursor-pointer rounded-lg border border-gray-300 bg-white px-4 py-2 text-sm text-gray-700 hover:bg-gray-50"
				>
					Cancel
				</button>
				<button
					type="button"
					onclick={confirmRotate}
					disabled={!rotateMatches || rotateBusy}
					class="cursor-pointer rounded-lg bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700 disabled:opacity-50 disabled:cursor-not-allowed"
				>
					{rotateBusy ? 'Rotating…' : 'Rotate now'}
				</button>
			</div>
		</div>
	</div>
{/if}
