<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type AdminProject, type AllowlistEntry, type TeamBetaEntry, type SignupUserEntry } from '$lib/api.js';

	let projects = $state<AdminProject[]>([]);
	let allowlist = $state<AllowlistEntry[]>([]);
	let teamBeta = $state<TeamBetaEntry[]>([]);
	let teamBetaSearch = $state('');
	let teamBetaBusy = $state(false);

	// Signup-users dashboard state (public-beta launch).
	let signupUsers = $state<SignupUserEntry[]>([]);
	let signupSearch = $state('');
	let signupToggleBusy = $state<string | null>(null); // user_id currently being toggled
	let signupFiltered = $derived(
		signupSearch.trim() === ''
			? signupUsers
			: signupUsers.filter((u) =>
					u.email.toLowerCase().includes(signupSearch.toLowerCase().trim())
				)
	);
	let mrrTotalCents = $derived(signupUsers.reduce((sum, u) => sum + u.mrr_cents, 0));
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
	// Closes #35. When some chunks fail (Scaleway TEM quota,
	// network blip), the API returns status=partial plus an `errors`
	// array. We capture it here so the operator can see WHICH chunks
	// didn't go out and retry just those — much better than the
	// pre-fix "send 9, deselect, send next 9, lather, rinse" loop.
	let composeFailures = $state<{ recipients: string[]; error: string }[]>([]);

	const INVITATION_TEMPLATE = {
		subject: "You're invited to Eurobase (closed beta)",
		body: `<p>Hi,</p>
<p>You're on the early-access list for <strong>Eurobase</strong> — the EU-sovereign backend-as-a-service, made in Berlin. We're opening the beta to a small cohort this week and you're in it.</p>
<p style="background:#fef3c7;border-left:3px solid #f59e0b;padding:10px 12px;margin:14px 0;color:#92400e;font-size:14px;"><strong>Heads-up — this is a beta test system.</strong> Features may change or break without notice. There are no uptime, data-retention, or backup guarantees during the beta. Please don't run production workloads on it yet.</p>
<p>To get started:</p>
<ol>
  <li>Go to <a href="https://console.eurobase.app">console.eurobase.app</a> and sign up with this email.</li>
  <li>Create your first project — the SDK (<code>npm install @eurobase/sdk</code>) gets you hacking in a minute.</li>
  <li>Read the <a href="https://console.eurobase.app/docs">docs</a> for auth, database, storage, and realtime examples.</li>
</ol>
<p>You stay in control of your data: you can delete a project (schema + storage objects) directly from the console at any time.</p>
<p>Found a bug or have an idea? File it on GitHub at <a href="https://github.com/STGime/euroback/issues">github.com/STGime/euroback/issues</a> — or just reply to this mail, I read every word.</p>
<p>— Stefan</p>
<hr/>
<p style="color:#6b7280;font-size:12px;">All your data stays in EU jurisdiction (Scaleway, France). GDPR by design.</p>`
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

	// Informational announcement — sends to every existing platform_user
	// (all of whom were invited via allowlist during closed beta). No
	// action required from them; public signup is now open for anyone
	// they want to share with. Ship the same hour as Deploy 1's env flip
	// (ALLOW_PUBLIC_SIGNUP=true / BILLING_ENABLED=true / MOLLIE_ENV=test).
	const PUBLIC_BETA_OPEN_TEMPLATE = {
		subject: 'Eurobase is now in public beta',
		body: `<p>Hi,</p>
<p>Quick note: <strong>Eurobase is now in public beta</strong>. You don't need to do anything — your account, projects, and data are unchanged.</p>
<p>What changed today:</p>
<ul>
  <li><strong>Signup is open to everyone</strong> at <a href="https://console.eurobase.app">console.eurobase.app</a>. Feel free to share.</li>
  <li><strong>Pro tier is live</strong> — €19/mo per project, upgrade any time from the console. Free stays free.</li>
  <li>Full write-up: <a href="https://eurobase.app/blog/public-beta-open">eurobase.app/blog/public-beta-open</a></li>
</ul>
<p>The closed-beta framing is over, but the practical reality is the same as yesterday: your data stays in EU jurisdiction (Scaleway, France), the SDK / CLI / MCP haven't changed, and <code>eurobase db dump</code> still gives you a standard <code>pg_dump</code> you can migrate anywhere.</p>
<p>Bug reports or feedback: reply to this mail, or open an issue at <a href="https://github.com/STGime/euroback/issues">github.com/STGime/euroback/issues</a>. I read every word.</p>
<p>— Stefan</p>
<hr/>
<p style="color:#6b7280;font-size:12px;">Sent to Eurobase account holders. This is not a marketing email — one-off announcement.</p>`
	};

	async function refresh() {
		loading = true;
		error = null;
		try {
			const [p, a, tb, su] = await Promise.all([
				api.adminListAllProjects(),
				api.adminListAllowlist(),
				api.adminListTeamBetaUsers(),
				api.adminListSignupUsers()
			]);
			projects = p.projects;
			allowlist = a.entries;
			teamBeta = tb.entries;
			signupUsers = su.users;
		} catch (e: any) {
			error = e?.message ?? 'Failed to load admin data';
		} finally {
			loading = false;
		}
	}

	// Team-tier closed-beta grant (M2).
	// Search-by-email + one-click grant. The "grant by email" flow
	// looks up the user in AdminListAllProjects — every project's
	// owner is a candidate, so scoping the search to "seen" owners
	// avoids surfacing users we've never heard of. If the target
	// isn't in that list yet, they need to sign up first.
	async function grantTeamBeta(email: string) {
		if (teamBetaBusy) return;
		const owner = projects.find((p) => p.owner_email.toLowerCase() === email.toLowerCase());
		if (!owner) {
			error = `No project owner found with email ${email}. Ask them to sign up first.`;
			return;
		}
		teamBetaBusy = true;
		error = null;
		try {
			await api.adminGrantTeamBeta(owner.owner_id);
			teamBetaSearch = '';
			await refresh();
		} catch (e: any) {
			error = e?.message ?? 'Grant failed';
		} finally {
			teamBetaBusy = false;
		}
	}

	async function revokeTeamBeta(entry: TeamBetaEntry) {
		if (
			!confirm(
				`Revoke Team-beta access for ${entry.email}?\n\n` +
					`They currently have ${entry.active_team_projects} active Team project(s). ` +
					`Revocation is prospective — existing projects keep running.`
			)
		)
			return;
		teamBetaBusy = true;
		error = null;
		try {
			await api.adminRevokeTeamBeta(entry.user_id);
			await refresh();
		} catch (e: any) {
			error = e?.message ?? 'Revoke failed';
		} finally {
			teamBetaBusy = false;
		}
	}

	// Per-row toggles for the signup-users table. Optimistic local
	// update + refetch on error keeps the UI snappy while still
	// converging on server truth if the toggle fails.
	async function toggleSignupTeamBeta(u: SignupUserEntry) {
		if (signupToggleBusy) return;
		signupToggleBusy = u.user_id;
		error = null;
		const prev = u.team_beta_access;
		try {
			u.team_beta_access = !prev; // optimistic
			if (prev) {
				await api.adminRevokeTeamBeta(u.user_id);
			} else {
				await api.adminGrantTeamBeta(u.user_id);
			}
		} catch (e: any) {
			u.team_beta_access = prev; // rollback
			error = e?.message ?? 'Toggle failed';
			await refresh();
		} finally {
			signupToggleBusy = null;
		}
	}

	async function toggleSignupLegalTeamBeta(u: SignupUserEntry) {
		if (signupToggleBusy) return;
		signupToggleBusy = u.user_id;
		error = null;
		const prev = u.legal_team_beta_access;
		try {
			u.legal_team_beta_access = !prev;
			if (prev) {
				await api.adminRevokeLegalTeamBeta(u.user_id);
			} else {
				await api.adminGrantLegalTeamBeta(u.user_id);
			}
		} catch (e: any) {
			u.legal_team_beta_access = prev;
			error = e?.message ?? 'Toggle failed';
			await refresh();
		} finally {
			signupToggleBusy = null;
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
		composeFailures = [];
		try {
			const res = await api.adminSendAllowlistEmail(recipients, composeSubject, composeBody);
			// Partial success: the server sent SOME chunks, others
			// failed. Show how many landed + surface the failing
			// chunks so the operator can retry just those addresses
			// instead of re-sending to everyone.
			if (res.status === 'partial') {
				composeSuccess = `Partial: ${res.sent} sent, ${res.failed} failed${res.bcc ? ' (BCC)' : ''}.`;
				composeFailures = res.errors ?? [];
				// Keep the failed addresses selected so the operator
				// can click Email again to retry just them.
				selected = new Set(composeFailures.flatMap((e) => e.recipients));
			} else {
				composeSuccess = `Sent to ${res.sent}${res.bcc ? ' (BCC)' : ''}.`;
				selected = new Set();
			}
			// Leave modal open so the user can see confirmation; they close manually.
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
		composeFailures = [];
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
		<div class="flex items-baseline justify-between">
			<h2 class="text-lg font-semibold text-gray-900">Team-tier closed beta</h2>
			<div class="text-xs text-gray-500">
				{teamBeta.length} granted ·
				{teamBeta.reduce((n, e) => n + e.active_team_projects, 0)} active Team projects
			</div>
		</div>
		<p class="text-xs text-gray-500">
			Granted users see the "Create Team project" CTA on their pricing page and can spin up
			dedicated managed-PG instances (fr-par). Revocation is prospective — existing Team
			projects keep running.
		</p>

		<div class="flex gap-2 items-end">
			<div class="flex-1">
				<label for="team-beta-email" class="text-xs text-gray-500 block mb-1">
					Grant by email (must be an existing project owner)
				</label>
				<input
					id="team-beta-email"
					type="email"
					bind:value={teamBetaSearch}
					placeholder="user@example.com"
					class="w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
				/>
			</div>
			<button
				onclick={() => grantTeamBeta(teamBetaSearch.trim())}
				disabled={teamBetaBusy || !teamBetaSearch.trim()}
				class="rounded-md bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 disabled:cursor-not-allowed disabled:opacity-40 cursor-pointer"
			>
				Grant
			</button>
		</div>

		<div class="rounded-md border border-gray-200 bg-white overflow-hidden">
			<table class="w-full text-sm">
				<thead class="bg-gray-50 text-left text-xs uppercase text-gray-500">
					<tr>
						<th class="px-4 py-2">User</th>
						<th class="px-4 py-2">Granted</th>
						<th class="px-4 py-2">Granted by</th>
						<th class="px-4 py-2">Active Team projects</th>
						<th class="px-4 py-2"></th>
					</tr>
				</thead>
				<tbody class="divide-y divide-gray-100">
					{#if loading}
						<tr><td colspan="5" class="px-4 py-6 text-center text-gray-400">Loading…</td></tr>
					{:else if teamBeta.length === 0}
						<tr><td colspan="5" class="px-4 py-6 text-center text-gray-400">No Team-beta grants yet.</td></tr>
					{:else}
						{#each teamBeta as e}
							<tr>
								<td class="px-4 py-2">
									<div class="font-mono text-gray-900">{e.email}</div>
									{#if e.display_name}
										<div class="text-xs text-gray-500">{e.display_name}</div>
									{/if}
								</td>
								<td class="px-4 py-2 text-gray-500">
									{e.granted_at ? new Date(e.granted_at).toLocaleDateString() : '—'}
								</td>
								<td class="px-4 py-2 text-gray-500">{e.granted_by_email ?? '—'}</td>
								<td class="px-4 py-2 text-gray-600">{e.active_team_projects}</td>
								<td class="px-4 py-2 text-right">
									<button
										onclick={() => revokeTeamBeta(e)}
										disabled={teamBetaBusy}
										class="text-red-600 hover:text-red-800 text-xs cursor-pointer disabled:cursor-not-allowed disabled:opacity-40"
									>
										Revoke
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
		<div class="flex items-center justify-between">
			<div>
				<h2 class="text-lg font-semibold text-gray-900">Signup Users</h2>
				<p class="text-xs text-gray-500">
					Every platform user, derived plan + MRR. Toggle Team / Legal-Team beta access per row.
				</p>
			</div>
			<div class="text-right">
				<div class="text-xs text-gray-500">Total MRR</div>
				<div class="text-lg font-semibold text-gray-900">
					€{(mrrTotalCents / 100).toFixed(2)}
				</div>
			</div>
		</div>
		<div>
			<input
				type="search"
				placeholder="Filter by email…"
				bind:value={signupSearch}
				class="w-full max-w-md rounded-md border border-gray-300 px-3 py-1.5 text-sm"
			/>
		</div>
		<div class="rounded-md border border-gray-200 bg-white overflow-hidden">
			<table class="w-full text-sm">
				<thead class="bg-gray-50 text-left text-xs uppercase text-gray-500">
					<tr>
						<th class="px-4 py-2">Email</th>
						<th class="px-4 py-2">Name</th>
						<th class="px-4 py-2">Signed up</th>
						<th class="px-4 py-2">Plan</th>
						<th class="px-4 py-2 text-right">MRR</th>
						<th class="px-4 py-2">Last active</th>
						<th class="px-4 py-2 text-right">Projects</th>
						<th class="px-4 py-2 text-center">Team beta</th>
						<th class="px-4 py-2 text-center">Legal Team beta</th>
					</tr>
				</thead>
				<tbody class="divide-y divide-gray-100">
					{#if loading}
						<tr><td colspan="9" class="px-4 py-6 text-center text-gray-400">Loading…</td></tr>
					{:else if signupFiltered.length === 0}
						<tr><td colspan="9" class="px-4 py-6 text-center text-gray-400">
							{signupSearch.trim() === '' ? 'No signups yet.' : 'No matches.'}
						</td></tr>
					{:else}
						{#each signupFiltered as u (u.user_id)}
							<tr>
								<td class="px-4 py-2 font-medium text-gray-900">{u.email}</td>
								<td class="px-4 py-2 text-gray-600">{u.display_name ?? '—'}</td>
								<td class="px-4 py-2 text-gray-500">
									{new Date(u.signup_date).toLocaleDateString('en-GB')}
								</td>
								<td class="px-4 py-2">
									{#if u.plan === 'pro'}
										<span class="inline-flex items-center rounded-full bg-emerald-100 px-2 py-0.5 text-xs font-medium text-emerald-800">Pro</span>
									{:else if u.checkout_pending}
										<span class="inline-flex items-center rounded-full bg-amber-100 px-2 py-0.5 text-xs font-medium text-amber-800" title="Checkout started, payment not yet confirmed">Pro (pending)</span>
									{:else}
										<span class="inline-flex items-center rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-700">Free</span>
									{/if}
								</td>
								<td class="px-4 py-2 text-right text-gray-700 font-mono">
									{u.mrr_cents > 0 ? `€${(u.mrr_cents / 100).toFixed(2)}` : '—'}
								</td>
								<td class="px-4 py-2 text-gray-500">
									{u.last_active_at ? new Date(u.last_active_at).toLocaleDateString('en-GB') : '—'}
								</td>
								<td class="px-4 py-2 text-right text-gray-700">{u.project_count}</td>
								<td class="px-4 py-2 text-center">
									<button
										type="button"
										onclick={() => toggleSignupTeamBeta(u)}
										disabled={signupToggleBusy === u.user_id}
										class="inline-flex items-center rounded px-2 py-0.5 text-xs font-medium transition-colors disabled:opacity-50 {u.team_beta_access
											? 'bg-blue-100 text-blue-800 hover:bg-blue-200'
											: 'bg-gray-100 text-gray-600 hover:bg-gray-200'}"
									>
										{u.team_beta_access ? '✓ Granted' : 'Grant'}
									</button>
								</td>
								<td class="px-4 py-2 text-center">
									<button
										type="button"
										onclick={() => toggleSignupLegalTeamBeta(u)}
										disabled={signupToggleBusy === u.user_id}
										class="inline-flex items-center rounded px-2 py-0.5 text-xs font-medium transition-colors disabled:opacity-50 {u.legal_team_beta_access
											? 'bg-purple-100 text-purple-800 hover:bg-purple-200'
											: 'bg-gray-100 text-gray-600 hover:bg-gray-200'}"
									>
										{u.legal_team_beta_access ? '✓ Granted' : 'Grant'}
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
					<span class="text-gray-300">·</span>
					<button
						type="button"
						class="text-eurobase-600 hover:underline cursor-pointer"
						onclick={() => applyTemplate(PUBLIC_BETA_OPEN_TEMPLATE)}
					>
						Public beta open
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
				{#if composeFailures.length > 0}
					<details class="rounded-md border border-amber-200 bg-amber-50 text-xs text-amber-900">
						<summary class="cursor-pointer px-3 py-2 font-medium">
							{composeFailures.length} chunk{composeFailures.length === 1 ? '' : 's'} failed — click Email to retry the still-selected addresses
						</summary>
						<ul class="border-t border-amber-200 px-3 py-2 space-y-1">
							{#each composeFailures as f}
								<li>
									<div class="font-mono text-[11px] text-amber-800">{f.recipients.join(', ')}</div>
									<div class="text-[11px] text-amber-700">{f.error}</div>
								</li>
							{/each}
						</ul>
					</details>
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
