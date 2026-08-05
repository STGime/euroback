<script lang="ts">
	import { page } from '$app/stores';
	import { onMount } from 'svelte';
	import { api, type ConnectionInfo, type Project } from '$lib/api.js';

	let projectId = $derived($page.params.id);
	let project = $state<Project | null>(null);
	let conn = $state<ConnectionInfo | null>(null);
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
			error = e?.message ?? 'Failed to load connection';
			conn = null;
		} finally {
			busy = false;
		}
	}

	onMount(() => load('readonly'));

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
					{#if role === 'readwrite'}
						<span class="text-red-600">Read/Write access allows destructive queries.</span>
					{/if}
				</p>
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
