<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type AdminProject, type AllowlistEntry } from '$lib/api.js';

	let projects = $state<AdminProject[]>([]);
	let allowlist = $state<AllowlistEntry[]>([]);
	let newEmail = $state('');
	let newNote = $state('');
	let loading = $state(true);
	let error = $state<string | null>(null);

	async function refresh() {
		loading = true;
		error = null;
		try {
			const [p, a] = await Promise.all([api.adminListAllProjects(), api.adminListAllowlist()]);
			projects = p.projects;
			allowlist = a.entries;
		} catch (e: any) {
			error = e?.message ?? 'Failed to load admin data';
		} finally {
			loading = false;
		}
	}

	onMount(refresh);

	async function addEntry() {
		if (!newEmail.trim()) return;
		try {
			await api.adminAddAllowlist(newEmail.trim(), newNote.trim() || undefined);
			newEmail = '';
			newNote = '';
			await refresh();
		} catch (e: any) {
			error = e?.message ?? 'Add failed';
		}
	}

	async function removeEntry(email: string) {
		if (!confirm(`Remove ${email} from the allowlist?`)) return;
		try {
			await api.adminRemoveAllowlist(email);
			await refresh();
		} catch (e: any) {
			error = e?.message ?? 'Remove failed';
		}
	}
</script>

<div class="max-w-6xl mx-auto space-y-8">
	<header>
		<h1 class="text-2xl font-semibold text-gray-900">Platform Admin</h1>
		<p class="text-sm text-gray-500 mt-1">
			Superadmin-only view of every project and the closed-beta allowlist.
		</p>
	</header>

	{#if error}
		<div class="rounded-md bg-red-50 border border-red-200 p-3 text-sm text-red-800">{error}</div>
	{/if}

	<section class="space-y-3">
		<h2 class="text-lg font-semibold text-gray-900">Signup Allowlist</h2>
		<div class="flex gap-2 items-end">
			<div class="flex-1">
				<label class="text-xs text-gray-500 block mb-1">Email</label>
				<input
					type="email"
					bind:value={newEmail}
					placeholder="user@example.com"
					class="w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
				/>
			</div>
			<div class="flex-1">
				<label class="text-xs text-gray-500 block mb-1">Note (optional)</label>
				<input
					type="text"
					bind:value={newNote}
					placeholder="beta tester, investor, …"
					class="w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
				/>
			</div>
			<button
				onclick={addEntry}
				class="rounded-md bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 cursor-pointer"
			>
				Add
			</button>
		</div>

		<div class="rounded-md border border-gray-200 bg-white overflow-hidden">
			<table class="w-full text-sm">
				<thead class="bg-gray-50 text-left text-xs uppercase text-gray-500">
					<tr>
						<th class="px-4 py-2">Email</th>
						<th class="px-4 py-2">Note</th>
						<th class="px-4 py-2">Added</th>
						<th class="px-4 py-2"></th>
					</tr>
				</thead>
				<tbody class="divide-y divide-gray-100">
					{#if loading}
						<tr><td colspan="4" class="px-4 py-6 text-center text-gray-400">Loading…</td></tr>
					{:else if allowlist.length === 0}
						<tr><td colspan="4" class="px-4 py-6 text-center text-gray-400">No allowlist entries yet.</td></tr>
					{:else}
						{#each allowlist as e}
							<tr>
								<td class="px-4 py-2 font-mono">{e.email}</td>
								<td class="px-4 py-2 text-gray-600">{e.note ?? ''}</td>
								<td class="px-4 py-2 text-gray-500">{new Date(e.created_at).toLocaleDateString()}</td>
								<td class="px-4 py-2 text-right">
									<button
										onclick={() => removeEntry(e.email)}
										class="text-red-600 hover:text-red-800 text-xs cursor-pointer"
									>
										Remove
									</button>
								</td>
							</tr>
						{/each}
					{/if}
				</tbody>
			</table>
		</div>
	</section>

	<section class="space-y-3">
		<h2 class="text-lg font-semibold text-gray-900">All Projects</h2>
		<div class="rounded-md border border-gray-200 bg-white overflow-hidden">
			<table class="w-full text-sm">
				<thead class="bg-gray-50 text-left text-xs uppercase text-gray-500">
					<tr>
						<th class="px-4 py-2">Project</th>
						<th class="px-4 py-2">Slug</th>
						<th class="px-4 py-2">Owner</th>
						<th class="px-4 py-2">Plan</th>
						<th class="px-4 py-2">Status</th>
						<th class="px-4 py-2">Created</th>
					</tr>
				</thead>
				<tbody class="divide-y divide-gray-100">
					{#if loading}
						<tr><td colspan="6" class="px-4 py-6 text-center text-gray-400">Loading…</td></tr>
					{:else if projects.length === 0}
						<tr><td colspan="6" class="px-4 py-6 text-center text-gray-400">No projects.</td></tr>
					{:else}
						{#each projects as p}
							<tr>
								<td class="px-4 py-2 font-medium text-gray-900">{p.name}</td>
								<td class="px-4 py-2 font-mono text-gray-600">{p.slug}</td>
								<td class="px-4 py-2 text-gray-600">{p.owner_email}</td>
								<td class="px-4 py-2 text-gray-600">{p.plan}</td>
								<td class="px-4 py-2 text-gray-600">{p.status}</td>
								<td class="px-4 py-2 text-gray-500">{new Date(p.created_at).toLocaleDateString()}</td>
							</tr>
						{/each}
					{/if}
				</tbody>
			</table>
		</div>
	</section>
</div>
