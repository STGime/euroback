<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type AdminProject, type AllowlistEntry } from '$lib/api.js';

	let projects = $state<AdminProject[]>([]);
	let allowlist = $state<AllowlistEntry[]>([]);
	let newEmail = $state('');
	let newNote = $state('');
	let loading = $state(true);
	let error = $state<string | null>(null);

	// Bulk-email selection state.
	let selected = $state<Set<string>>(new Set());
	let composeOpen = $state(false);
	let composeSubject = $state('');
	let composeBody = $state('');
	let composeBusy = $state(false);
	let composeError = $state<string | null>(null);
	let composeSuccess = $state<string | null>(null);

	const INVITATION_TEMPLATE = {
		subject: "You're invited to Eurobase (closed beta)",
		body: `<p>Hi,</p>
<p>You're on the early-access list for <strong>Eurobase</strong> — the EU-sovereign backend-as-a-service built in France. We're opening the beta to a small cohort this week and you're in it.</p>
<p>To get started:</p>
<ol>
  <li>Go to <a href="https://console.eurobase.app">console.eurobase.app</a> and sign up with this email.</li>
  <li>Create your first project — the CLI (<code>npm install -g @eurobase/sdk</code>) gets you hacking in a minute.</li>
  <li>Read the <a href="https://console.eurobase.app/docs">docs</a> for auth, database, storage, and realtime examples.</li>
</ol>
<p>If anything breaks or you have questions, just reply — I read every mail personally.</p>
<p>— Stefan</p>
<hr/>
<p style="color:#6b7280;font-size:12px;">All your data stays in EU jurisdiction (Scaleway, France). GDPR by design. No US-CLOUD-Act exposure.</p>`
	};

	const UPDATE_TEMPLATE = {
		subject: 'Eurobase update — what shipped this week',
		body: `<p>Hi,</p>
<p>Short update on what's new in Eurobase since you last logged in:</p>
<ul>
  <li><strong>Feature 1</strong> — ...</li>
  <li><strong>Feature 2</strong> — ...</li>
</ul>
<p>Try it out at <a href="https://console.eurobase.app">console.eurobase.app</a>. As always, reply with feedback.</p>
<p>— Stefan</p>`
	};

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
			selected.delete(email);
			selected = new Set(selected);
			await refresh();
		} catch (e: any) {
			error = e?.message ?? 'Remove failed';
		}
	}

	function toggleOne(email: string) {
		if (selected.has(email)) selected.delete(email);
		else selected.add(email);
		selected = new Set(selected);
	}

	let allSelected = $derived(allowlist.length > 0 && selected.size === allowlist.length);
	let someSelected = $derived(selected.size > 0 && selected.size < allowlist.length);

	function toggleAll() {
		if (allSelected) {
			selected = new Set();
		} else {
			selected = new Set(allowlist.map((e) => e.email));
		}
	}

	function openCompose() {
		composeError = null;
		composeSuccess = null;
		composeOpen = true;
	}

	function applyTemplate(tpl: { subject: string; body: string }) {
		composeSubject = tpl.subject;
		composeBody = tpl.body;
	}

	async function sendCompose() {
		composeError = null;
		composeSuccess = null;
		const recipients = Array.from(selected);
		if (recipients.length === 0) {
			composeError = 'No recipients selected';
			return;
		}
		if (!composeSubject.trim() || !composeBody.trim()) {
			composeError = 'Subject and body are required';
			return;
		}
		if (
			!confirm(
				`Send to ${recipients.length} recipient${recipients.length === 1 ? '' : 's'}${recipients.length > 1 ? ' (BCC)' : ''}?`
			)
		)
			return;
		composeBusy = true;
		try {
			const res = await api.adminSendAllowlistEmail(recipients, composeSubject, composeBody);
			composeSuccess = `Sent to ${res.sent}${res.bcc ? ' (BCC)' : ''}.`;
			// Leave modal open so the user can see confirmation; they close manually.
			selected = new Set();
		} catch (e: any) {
			composeError = e?.message ?? 'Send failed';
		} finally {
			composeBusy = false;
		}
	}

	function closeCompose() {
		composeOpen = false;
		composeError = null;
		composeSuccess = null;
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

		<div class="flex items-center justify-between">
			<div class="text-xs text-gray-500">
				{selected.size === 0 ? 'Select recipients with the checkboxes to send an email.' : `${selected.size} selected`}
			</div>
			<button
				type="button"
				onclick={openCompose}
				disabled={selected.size === 0}
				class="rounded-md bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 disabled:cursor-not-allowed disabled:opacity-40 cursor-pointer"
			>
				Email {selected.size > 0 ? `(${selected.size})` : ''}
			</button>
		</div>

		<div class="rounded-md border border-gray-200 bg-white overflow-hidden">
			<table class="w-full text-sm">
				<thead class="bg-gray-50 text-left text-xs uppercase text-gray-500">
					<tr>
						<th class="w-10 px-4 py-2">
							<input
								type="checkbox"
								checked={allSelected}
								indeterminate={someSelected}
								onchange={toggleAll}
								aria-label="Select all recipients"
								class="h-4 w-4 cursor-pointer"
							/>
						</th>
						<th class="px-4 py-2">Email</th>
						<th class="px-4 py-2">Note</th>
						<th class="px-4 py-2">Added</th>
						<th class="px-4 py-2"></th>
					</tr>
				</thead>
				<tbody class="divide-y divide-gray-100">
					{#if loading}
						<tr><td colspan="5" class="px-4 py-6 text-center text-gray-400">Loading…</td></tr>
					{:else if allowlist.length === 0}
						<tr><td colspan="5" class="px-4 py-6 text-center text-gray-400">No allowlist entries yet.</td></tr>
					{:else}
						{#each allowlist as e}
							<tr class={selected.has(e.email) ? 'bg-eurobase-50/60' : ''}>
								<td class="px-4 py-2">
									<input
										type="checkbox"
										checked={selected.has(e.email)}
										onchange={() => toggleOne(e.email)}
										aria-label={`Select ${e.email}`}
										class="h-4 w-4 cursor-pointer"
									/>
								</td>
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

<!-- Compose modal -->
{#if composeOpen}
	<div
		class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
		role="dialog"
		aria-modal="true"
	>
		<div class="bg-white rounded-lg shadow-xl w-full max-w-2xl max-h-[90vh] overflow-y-auto">
			<div class="flex items-center justify-between border-b border-gray-200 px-5 py-3">
				<div>
					<h3 class="text-base font-semibold text-gray-900">Email recipients</h3>
					<p class="text-xs text-gray-500 mt-0.5">
						{selected.size} selected{selected.size > 1 ? ' · delivered via BCC' : ''}
					</p>
				</div>
				<button
					type="button"
					onclick={closeCompose}
					aria-label="Close"
					class="text-gray-400 hover:text-gray-600 cursor-pointer text-xl leading-none"
				>
					&times;
				</button>
			</div>

			<div class="p-5 space-y-4">
				<div class="flex gap-2 text-xs">
					<span class="text-gray-500">Presets:</span>
					<button
						type="button"
						class="text-eurobase-600 hover:underline cursor-pointer"
						onclick={() => applyTemplate(INVITATION_TEMPLATE)}
					>
						Beta invitation
					</button>
					<span class="text-gray-300">·</span>
					<button
						type="button"
						class="text-eurobase-600 hover:underline cursor-pointer"
						onclick={() => applyTemplate(UPDATE_TEMPLATE)}
					>
						Product update
					</button>
				</div>

				<div>
					<label class="block text-xs text-gray-500 mb-1">Subject</label>
					<input
						type="text"
						bind:value={composeSubject}
						placeholder="You're invited to Eurobase"
						class="w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
					/>
				</div>
				<div>
					<label class="block text-xs text-gray-500 mb-1">Body (HTML)</label>
					<textarea
						bind:value={composeBody}
						rows="12"
						placeholder="<p>Hi,</p><p>…</p>"
						class="w-full rounded-md border border-gray-300 px-3 py-2 text-xs font-mono"
					></textarea>
					<p class="mt-1 text-xs text-gray-400">Paste or write HTML. Plain text is fine too — just use &lt;p&gt; tags for line breaks.</p>
				</div>

				<details class="rounded-md border border-gray-200 bg-gray-50">
					<summary class="cursor-pointer px-3 py-2 text-xs font-medium text-gray-600">Preview</summary>
					<div class="border-t border-gray-200 bg-white p-4 text-sm">
						<div class="text-xs text-gray-500 mb-2 font-mono">Subject: {composeSubject || '(empty)'}</div>
						<!-- eslint-disable-next-line svelte/no-at-html-tags -->
						<div class="prose prose-sm max-w-none">{@html composeBody || '<em>Nothing to preview</em>'}</div>
					</div>
				</details>

				{#if composeError}
					<div class="rounded-md bg-red-50 border border-red-200 p-2 text-xs text-red-800">{composeError}</div>
				{/if}
				{#if composeSuccess}
					<div class="rounded-md bg-green-50 border border-green-200 p-2 text-xs text-green-800">{composeSuccess}</div>
				{/if}
			</div>

			<div class="flex items-center justify-end gap-2 border-t border-gray-200 px-5 py-3">
				<button
					type="button"
					onclick={closeCompose}
					class="rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-700 hover:bg-gray-50 cursor-pointer"
				>
					Close
				</button>
				<button
					type="button"
					onclick={sendCompose}
					disabled={composeBusy || composeSuccess !== null}
					class="rounded-md bg-eurobase-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-eurobase-700 disabled:cursor-not-allowed disabled:opacity-40 cursor-pointer"
				>
					{composeBusy ? 'Sending…' : 'Send'}
				</button>
			</div>
		</div>
	</div>
{/if}
