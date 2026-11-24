<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type AdminProject, type BlockedDomainEntry, type TeamBetaEntry, type SignupUserEntry, type BroadcastRecipient } from '$lib/api.js';

	let projects = $state<AdminProject[]>([]);
	let blockedDomains = $state<BlockedDomainEntry[]>([]);
	let newBlockedDomain = $state('');
	let newBlockedReason = $state('');
	let blockedBusy = $state(false);

	// Top-of-page section navigation. The admin view is long (projects,
	// users, blocked domains, team beta, contact triage), so
	// instead of one endless scroll we show one section at a time behind a
	// tab strip. The active tab is mirrored to the URL hash so a view is
	// shareable / survives a reload. All data still loads once in refresh().
	const adminTabs = [
		{ id: 'projects', label: 'Projects' },
		{ id: 'users', label: 'Signup users' },
		{ id: 'blocked', label: 'Blocked domains' },
		{ id: 'teambeta', label: 'Team beta' },
		{ id: 'contact', label: 'Contact' }
	] as const;
	let activeTab = $state<string>('projects');
	function setTab(id: string) {
		activeTab = id;
		if (typeof history !== 'undefined') history.replaceState(null, '', `#${id}`);
	}
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
	let loading = $state(true);
	let error = $state<string | null>(null);

	// Compose state.
	let composeOpen = $state(false);
	let composeSubject = $state('');
	let composeBody = $state('');
	let composeBusy = $state(false);
	let composeError = $state<string | null>(null);
	let composeSuccess = $state<string | null>(null);
	// composeMode decides the recipients:
	//   'signup-selected' — the rows ticked in the Signup users table.
	//   'audience'        — every signed-up account that gets product
	//                       updates (server-computed audience, accounts only).
	// Both POST to the same endpoint; the server re-checks every address
	// (verified, not unsubscribed, not bounced) and sends one message per
	// recipient in the background.
	type ComposeMode = 'audience' | 'signup-selected';
	let composeMode = $state<ComposeMode>('signup-selected');

	// Manual per-row selection for the Signup Users table.
	let selectedSignups = $state<Set<string>>(new Set());
	let broadcastAudience = $state<BroadcastRecipient[]>([]);
	let broadcastLoaded = $state(false);
	let broadcastLoading = $state(false);
	let broadcastError = $state<string | null>(null);

	// Broadcast recipients: signed-up accounts only (the invite list is
	// no longer used; signup is open).
	let audienceRecipients = $derived.by(() => {
		if (composeMode !== 'audience') return [] as string[];
		return broadcastAudience.filter((r) => r.has_account).map((r) => r.email);
	});
	let audienceRecipientCount = $derived(audienceRecipients.length);

	// Team-tier closed-beta invitation. Pro has been live since public
	// beta (see PUBLIC_BETA_OPEN_TEMPLATE), so the generic "closed beta"
	// invite is retired — the thing we now hand-invite people into is
	// Team. Content tracks docs chapter 26 (Team tier) + 27 (Orgs & SSO):
	// grant-first flow (ops flips team_beta_access, then this mail tells
	// them), free during the beta window, 30 days' notice before billing.
	const TEAM_INVITATION_TEMPLATE = {
		subject: "You're in the Eurobase Team beta",
		body: `<p>Hi,</p>
<p>We've enabled <strong>Team-tier beta access</strong> on your Eurobase account. Team is for workloads that have outgrown the shared cluster and want production isolation — your own database, backups you control, and company-wide login.</p>
<p style="background:#ecfdf5;border-left:3px solid #10b981;padding:10px 12px;margin:14px 0;color:#065f46;font-size:14px;"><strong>Free during the closed beta.</strong> Team isn't self-serve on the pricing page yet — grants are manual, and we'll give you <strong>30 days' notice before any billing starts</strong>. Post-beta pricing is €149/mo per organisation (SSO, RBAC, priority support, and one bundled Team project), with extra projects at their per-project rate.</p>
<p><strong>What Team gives you, on top of Pro:</strong></p>
<ul>
  <li><strong>A dedicated Postgres instance</strong> (Scaleway fr-par) per Team project — no shared cluster, no noisy neighbours.</li>
  <li><strong>Automatic daily backups</strong> with 7-day retention, plus <strong>on-demand snapshots</strong> you can tag before a risky migration.</li>
  <li><strong>Self-serve restore</strong> from any snapshot — no support ticket. One restore per calendar month is included.</li>
  <li><strong>A direct <code>DATABASE_URL</code></strong> so Prisma, Drizzle, <code>psql</code>, or any migration runner can connect straight to the instance.</li>
  <li><strong>Organizations with OIDC SSO</strong> — sign your team in through your own identity provider (Google Workspace, Microsoft Entra ID, Okta, Authentik, …) and invite teammates by email.</li>
  <li><strong>Priority support</strong> on Team-tier tickets.</li>
</ul>
<p><strong>To get started:</strong></p>
<ol>
  <li>Sign in at <a href="https://console.eurobase.app">console.eurobase.app</a> with this email — you'll now see <strong>Team</strong> in the new-project modal and <strong>Organizations</strong> in the sidebar.</li>
  <li><strong>Projects → New project → Team</strong>. We provision a dedicated instance in the background (usually 90 seconds to a few minutes).</li>
  <li>Walk the full flow in the docs: <a href="https://console.eurobase.app/docs/team-tier">Team tier</a> and <a href="https://console.eurobase.app/docs/orgs-sso">Organizations &amp; SSO</a>.</li>
</ol>
<p style="background:#fef3c7;border-left:3px solid #f59e0b;padding:10px 12px;margin:14px 0;color:#92400e;font-size:14px;"><strong>A few beta rough edges to set expectations:</strong> SSO is OIDC-only for now (no SAML) and teammates are added by email (no domain auto-provisioning yet); one organisation per account for now; snapshots are point-in-time restore points, not continuous PITR, and don't include your S3 storage objects; there's no self-serve billing or Team→Free/Pro downgrade during the beta — once a project is on a dedicated instance it stays there. Features may still change.</p>
<p>Found a bug or have an idea? File it on GitHub at <a href="https://github.com/STGime/euroback/issues">github.com/STGime/euroback/issues</a> — or write to <a href="mailto:contact@eurobase.app">contact@eurobase.app</a>, I read every word.</p>
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
<p>Try it out at <a href="https://console.eurobase.app">console.eurobase.app</a>. As always, send feedback to <a href="mailto:contact@eurobase.app">contact@eurobase.app</a>.</p>
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
  <li><strong>Pro tier is live</strong> — €25/mo per project, upgrade any time from the console. Free stays free (personal + development use only).</li>
  <li>Full write-up: <a href="https://eurobase.app/blog/public-beta-open">eurobase.app/blog/public-beta-open</a></li>
</ul>
<p>The closed-beta framing is over, but the practical reality is the same as yesterday: your data stays in EU jurisdiction (Scaleway, France), the SDK / CLI / MCP haven't changed, and <code>eurobase db dump</code> still gives you a standard <code>pg_dump</code> you can migrate anywhere.</p>
<p>Bug reports or feedback: write to <a href="mailto:contact@eurobase.app">contact@eurobase.app</a>, or open an issue at <a href="https://github.com/STGime/euroback/issues">github.com/STGime/euroback/issues</a>. I read every word.</p>
<p>— Stefan</p>
<hr/>
<p style="color:#6b7280;font-size:12px;">Sent to Eurobase account holders. This is not a marketing email — one-off announcement.</p>`
	};

	// Current superadmin's user_id — read once at load so the
	// Signup Users table can hide the delete button on the row that
	// represents the operator themselves. The backend refuses
	// self-delete regardless; this is UX polish.
	let currentUserId = $state<string>('');

	async function refresh() {
		loading = true;
		error = null;
		try {
			const [p, bd, tb, su, profile] = await Promise.all([
				api.adminListAllProjects(),
				api.adminListBlockedDomains(),
				api.adminListTeamBetaUsers(),
				api.adminListSignupUsers(),
				api.getProfile(),
			]);
			projects = p.projects;
			blockedDomains = bd.entries;
			teamBeta = tb.entries;
			signupUsers = su.users;
			// Drop selections that no longer get product updates (their
			// checkbox is disabled, so they couldn't be unticked).
			const mailable = new Set(su.users.filter((u) => u.mailable).map((u) => u.email));
			selectedSignups = new Set([...selectedSignups].filter((e) => mailable.has(e)));
			currentUserId = profile.id;
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

	// Delete-user modal state. Two-step confirm: click trash → modal
	// showing email + project count, operator types the email to
	// confirm → destructive call. The backend does the actual
	// project-by-project teardown (including Scaleway for Team-tier)
	// so this handler just fires the request and reloads.
	let deleteUserTarget = $state<SignupUserEntry | null>(null);
	let deleteUserConfirmInput = $state('');
	let deleteUserBusy = $state(false);

	function openDeleteUserModal(u: SignupUserEntry) {
		deleteUserTarget = u;
		deleteUserConfirmInput = '';
	}

	function closeDeleteUserModal() {
		if (deleteUserBusy) return;
		deleteUserTarget = null;
		deleteUserConfirmInput = '';
	}

	async function confirmDeleteUser() {
		if (!deleteUserTarget || deleteUserBusy) return;
		if (deleteUserConfirmInput.trim().toLowerCase() !== deleteUserTarget.email.toLowerCase()) return;
		deleteUserBusy = true;
		error = null;
		try {
			const target = deleteUserTarget;
			await api.adminDeleteUser(target.user_id);
			deleteUserTarget = null;
			deleteUserConfirmInput = '';
			await refresh();
		} catch (e: any) {
			error = e?.message ?? 'Delete failed';
		} finally {
			deleteUserBusy = false;
		}
	}

	// Project suspension: a confirm dialog for suspending (it takes the
	// project offline); unsuspending is immediate.
	let suspendTarget = $state<AdminProject | null>(null);
	let suspendReason = $state('');
	let suspendError = $state<string | null>(null);
	let projectBusy = $state<string | null>(null);

	async function confirmSuspendProject() {
		if (!suspendTarget || projectBusy) return;
		const target = suspendTarget;
		projectBusy = target.id;
		suspendError = null;
		try {
			await api.adminSuspendProject(target.id, suspendReason.trim());
			suspendTarget = null;
			suspendReason = '';
			await refresh();
		} catch (e: any) {
			// Shown in the dialog: the page banner sits behind its overlay.
			suspendError = e?.message ?? 'Suspend failed';
		} finally {
			projectBusy = null;
		}
	}

	async function unsuspendProject(p: AdminProject) {
		if (projectBusy) return;
		projectBusy = p.id;
		error = null;
		try {
			await api.adminUnsuspendProject(p.id);
			await refresh();
		} catch (e: any) {
			error = e?.message ?? 'Unsuspend failed';
		} finally {
			projectBusy = null;
		}
	}

	async function extendTeamBeta(p: AdminProject) {
		if (projectBusy) return;
		const until = new Date(Math.max(new Date(p.beta_ends_at ?? 0).getTime(), Date.now()) + 14 * 86400000).toLocaleDateString();
		if (!confirm(`Extend the Team beta of ${p.slug} to ${until}?${p.beta_expired_at ? ' This also reactivates the project.' : ''}`)) return;
		projectBusy = p.id;
		error = null;
		try {
			await api.adminExtendTeamBeta(p.id);
			await refresh();
		} catch (e: any) {
			error = e?.message ?? 'Extend failed';
		} finally {
			projectBusy = null;
		}
	}

	onMount(refresh);
	onMount(() => {
		// Deep-link / reload support: open the tab named in the URL hash.
		const h = typeof location !== 'undefined' ? location.hash.replace(/^#/, '') : '';
		if (adminTabs.some((t) => t.id === h)) activeTab = h;
	});

	async function addBlockedDomain() {
		if (!newBlockedDomain.trim() || blockedBusy) return;
		blockedBusy = true;
		error = null;
		try {
			await api.adminAddBlockedDomain(newBlockedDomain.trim(), newBlockedReason.trim() || undefined);
			newBlockedDomain = '';
			newBlockedReason = '';
			await refresh();
		} catch (e: any) {
			error = e?.message ?? 'Failed to block domain';
		} finally {
			blockedBusy = false;
		}
	}

	async function removeBlockedDomain(domain: string) {
		if (blockedBusy || !confirm(`Unblock ${domain}? New signups from this domain will be allowed again.`)) return;
		blockedBusy = true;
		error = null;
		try {
			await api.adminRemoveBlockedDomain(domain);
			await refresh();
		} catch (e: any) {
			error = e?.message ?? 'Failed to unblock domain';
		} finally {
			blockedBusy = false;
		}
	}

	function toggleOneSignup(email: string) {
		if (selectedSignups.has(email)) selectedSignups.delete(email);
		else selectedSignups.add(email);
		selectedSignups = new Set(selectedSignups);
	}

	// All / some for the CURRENT filter (so "select all" respects the
	// visible rows, not the whole dataset). Signup-row selection uses
	// email as the key so it dedupes across a re-fetch.
	// Only rows that product updates reach can be selected (verified,
	// not opted out, not bounced); the server applies the same rules.
	let signupMailable = $derived(signupFiltered.filter((u) => u.mailable));
	let allSignupsSelected = $derived(
		signupMailable.length > 0 &&
			signupMailable.every((u) => selectedSignups.has(u.email))
	);
	let someSignupsSelected = $derived(
		selectedSignups.size > 0 && !allSignupsSelected
	);

	function toggleAllSignups() {
		if (allSignupsSelected) {
			// Clear only the currently-visible-and-selected rows so
			// selections made under a different filter are preserved.
			const next = new Set(selectedSignups);
			for (const u of signupMailable) next.delete(u.email);
			selectedSignups = next;
		} else {
			const next = new Set(selectedSignups);
			for (const u of signupMailable) next.add(u.email);
			selectedSignups = next;
		}
	}

	// Addresses the server left out: not eligible for product updates.
	function skippedNote(n: number | undefined): string {
		return n ? ` ${n} left out (unverified, unsubscribed, bounced or unknown).` : '';
	}

	function openSignupCompose() {
		composeError = null;
		composeSuccess = null;
		composeMode = 'signup-selected';
		composeOpen = true;
	}

	// Open the compose modal in broadcast mode. Fetches the audience
	// lazily on first open so the /admin page load isn't blocked on
	// a query that's only needed if the operator clicks Broadcast.
	// Subsequent opens reuse the cached audience — a superadmin
	// doing multiple sends in one session doesn't re-hit the DB
	// unless they refresh the page.
	async function openBroadcast() {
		composeError = null;
		composeSuccess = null;
		composeMode = 'audience';
		composeOpen = true;
		if (!broadcastLoaded && !broadcastLoading) {
			broadcastLoading = true;
			broadcastError = null;
			try {
				const res = await api.adminGetBroadcastAudience();
				broadcastAudience = res.recipients;
				broadcastLoaded = true;
			} catch (e: any) {
				broadcastError = e?.message ?? 'Failed to load broadcast audience';
			} finally {
				broadcastLoading = false;
			}
		}
	}

	function applyTemplate(tpl: { subject: string; body: string }) {
		composeSubject = tpl.subject;
		composeBody = tpl.body;
	}

	async function sendCompose() {
		composeError = null;
		composeSuccess = null;
		const recipients =
			composeMode === 'audience' ? audienceRecipients : Array.from(selectedSignups);
		if (recipients.length === 0) {
			composeError =
				composeMode === 'audience'
					? 'No signed-up account gets product updates'
					: 'No recipients selected';
			return;
		}
		if (!composeSubject.trim() || !composeBody.trim()) {
			composeError = 'Subject and body are required';
			return;
		}
		const audienceLabel = composeMode === 'audience' ? ' (broadcast to all signed-up users)' : '';
		if (
			!confirm(
				`Send to ${recipients.length} recipient${recipients.length === 1 ? '' : 's'}${audienceLabel}? Each recipient gets their own copy.`
			)
		)
			return;
		composeBusy = true;
		try {
			const res = await api.adminSendAllowlistEmail(recipients, composeSubject, composeBody);
			// Sending runs in the background (one message per recipient);
			// the outcome, including failed addresses, is in the audit log
			// (broadcast.emailed).
			composeSuccess = `Sending to ${res.recipients} recipient${res.recipients === 1 ? '' : 's'} in the background. The result appears in the audit log.${skippedNote(res.skipped)}`;
			if (composeMode === 'signup-selected') {
				selectedSignups = new Set();
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
	}
</script>

<div class="max-w-6xl mx-auto space-y-8">
	<header class="flex items-center justify-between">
		<div>
			<h1 class="text-2xl font-semibold text-gray-900">Platform Admin</h1>
			<p class="text-sm text-gray-500 mt-1">
				Superadmin-only view of every project and every signed-up user.
			</p>
		</div>
		<nav class="text-sm space-x-3">
			<a href="/admin/inbox" class="text-blue-600 hover:underline">
				Inbox →
			</a>
			<a href="/admin/upgrades" class="text-blue-600 hover:underline">
				Team-tier upgrades →
			</a>
			<a href="/admin/sql" class="text-blue-600 hover:underline">
				SQL log →
			</a>
		</nav>
	</header>

	{#if error}
		<div class="rounded-md bg-red-50 border border-red-200 p-3 text-sm text-red-800">{error}</div>
	{/if}

	<!-- Section navigation: one section at a time behind a tab strip. -->
	<nav class="flex gap-1 overflow-x-auto md:flex-wrap md:overflow-visible border-b border-gray-200">
		{#each adminTabs as t}
			<button
				type="button"
				onclick={() => setTab(t.id)}
				aria-current={activeTab === t.id ? 'page' : undefined}
				class="relative shrink-0 whitespace-nowrap px-3 sm:px-4 py-2.5 text-sm font-medium transition-colors cursor-pointer {activeTab === t.id ? 'text-eurobase-700' : 'text-gray-500 hover:text-gray-700'}"
			>
				{t.label}
				{#if activeTab === t.id}
					<span class="absolute bottom-0 left-0 right-0 h-0.5 bg-eurobase-600 rounded-full"></span>
				{/if}
			</button>
		{/each}
	</nav>

	{#if activeTab === 'blocked'}
	<section class="space-y-3">
		<h2 class="text-lg font-semibold text-gray-900">Blocked signup domains</h2>
		<p class="text-xs text-gray-500">
			New platform signups from these domains (and their subdomains) are refused. Use this for
			abuse / throwaway domains. It does not affect existing accounts — suspend or delete those from
			Signup Users below.
		</p>
		<div class="flex gap-2 items-end">
			<div class="flex-1">
				<label for="blocked-new-domain" class="text-xs text-gray-500 block mb-1">Domain</label>
				<input
					id="blocked-new-domain"
					type="text"
					bind:value={newBlockedDomain}
					placeholder="example.com"
					onkeydown={(e) => e.key === 'Enter' && addBlockedDomain()}
					class="w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
				/>
			</div>
			<div class="flex-1">
				<label for="blocked-new-reason" class="text-xs text-gray-500 block mb-1">Reason (optional)</label>
				<input
					id="blocked-new-reason"
					type="text"
					bind:value={newBlockedReason}
					placeholder="abuse, throwaway signups, …"
					onkeydown={(e) => e.key === 'Enter' && addBlockedDomain()}
					class="w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
				/>
			</div>
			<button
				onclick={addBlockedDomain}
				disabled={blockedBusy || !newBlockedDomain.trim()}
				class="rounded-md bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700 disabled:cursor-not-allowed disabled:opacity-40 cursor-pointer"
			>
				Block
			</button>
		</div>

		<div class="rounded-md border border-gray-200 bg-white overflow-x-auto">
			<table class="w-full text-sm">
				<thead class="bg-gray-50 text-left text-xs uppercase text-gray-500">
					<tr>
						<th class="px-4 py-2">Domain</th>
						<th class="px-4 py-2">Reason</th>
						<th class="px-4 py-2">Added</th>
						<th class="px-4 py-2"></th>
					</tr>
				</thead>
				<tbody class="divide-y divide-gray-100">
					{#if loading}
						<tr><td colspan="4" class="px-4 py-6 text-center text-gray-400">Loading…</td></tr>
					{:else if blockedDomains.length === 0}
						<tr><td colspan="4" class="px-4 py-6 text-center text-gray-400">No blocked domains.</td></tr>
					{:else}
						{#each blockedDomains as d}
							<tr>
								<td class="px-4 py-2 font-mono text-gray-900">{d.domain}</td>
								<td class="px-4 py-2 text-gray-600">{d.reason ?? ''}</td>
								<td class="px-4 py-2 text-gray-500">{new Date(d.created_at).toLocaleDateString()}</td>
								<td class="px-4 py-2 text-right">
									<button
										type="button"
										onclick={() => removeBlockedDomain(d.domain)}
										disabled={blockedBusy}
										class="text-xs font-medium text-red-600 hover:text-red-700 disabled:opacity-40 cursor-pointer"
									>
										Unblock
									</button>
								</td>
							</tr>
						{/each}
					{/if}
				</tbody>
			</table>
		</div>
	</section>
	{/if}

	{#if activeTab === 'teambeta'}
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

		<div class="rounded-md border border-gray-200 bg-white overflow-x-auto">
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
	{/if}

	{#if activeTab === 'users'}
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
		<div class="flex items-center justify-between gap-3 flex-wrap">
			<input
				type="search"
				placeholder="Filter by email…"
				bind:value={signupSearch}
				class="w-full max-w-md rounded-md border border-gray-300 px-3 py-1.5 text-sm"
			/>
			<div class="flex gap-2">
			<button
				type="button"
				onclick={openBroadcast}
				class="rounded-lg border border-eurobase-300 bg-white px-3 py-2 text-sm font-semibold text-eurobase-700 hover:bg-eurobase-50 cursor-pointer transition-colors"
				title="Email every signed-up user who gets product updates (verified, not unsubscribed, not bounced)."
			>
				Compose broadcast
			</button>
			<button
				type="button"
				onclick={openSignupCompose}
				disabled={selectedSignups.size === 0}
				class="inline-flex items-center gap-2 rounded-lg bg-eurobase-600 px-3 py-2 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
			>
				Email {selectedSignups.size > 0 ? `(${selectedSignups.size})` : 'selected'}
			</button>
			</div>
		</div>
		<!-- Signup Users table wrapper needs overflow-x-auto (not
		     overflow-hidden used elsewhere in this page): at 11 columns
		     the last column (Actions / delete) clips off-screen on
		     narrower viewports with no way to scroll to it. -->
		<div class="rounded-md border border-gray-200 bg-white overflow-x-auto">
			<table class="w-full text-sm">
				<thead class="bg-gray-50 text-left text-xs uppercase text-gray-500">
					<tr>
						<th class="px-3 py-2 w-8">
							<input
								type="checkbox"
								checked={allSignupsSelected}
								indeterminate={someSignupsSelected}
								onchange={toggleAllSignups}
								aria-label={allSignupsSelected ? 'Deselect all visible signup users' : 'Select all visible signup users'}
								class="h-4 w-4 rounded border-gray-300 text-eurobase-600 focus:ring-eurobase-500"
							/>
						</th>
						<th class="px-4 py-2">Email</th>
						<th class="px-4 py-2">Name</th>
						<th class="px-4 py-2">Signed up</th>
						<th class="px-4 py-2">Plan</th>
						<th class="px-4 py-2 text-right">MRR</th>
						<th class="px-4 py-2">Last active</th>
						<th class="px-4 py-2 text-right">Projects</th>
						<th class="px-4 py-2 text-center">Team beta</th>
						<th class="px-4 py-2 text-center">Legal Team beta</th>
						<th class="px-4 py-2 text-center w-16">Actions</th>
					</tr>
				</thead>
				<tbody class="divide-y divide-gray-100">
					{#if loading}
						<tr><td colspan="11" class="px-4 py-6 text-center text-gray-400">Loading…</td></tr>
					{:else if signupFiltered.length === 0}
						<tr><td colspan="11" class="px-4 py-6 text-center text-gray-400">
							{signupSearch.trim() === '' ? 'No signups yet.' : 'No matches.'}
						</td></tr>
					{:else}
						{#each signupFiltered as u (u.user_id)}
							<tr class={selectedSignups.has(u.email) ? 'bg-eurobase-50/60' : ''}>
								<td class="px-3 py-2">
									<input
										type="checkbox"
										checked={selectedSignups.has(u.email)}
										disabled={!u.mailable}
										title={u.mailable ? undefined : 'No product updates: unverified, unsubscribed or bounced'}
										onchange={() => toggleOneSignup(u.email)}
										aria-label={`Select ${u.email}`}
										class="h-4 w-4 rounded border-gray-300 text-eurobase-600 focus:ring-eurobase-500"
									/>
								</td>
								<td class="px-4 py-2 font-medium text-gray-900">
									{u.email}
									{#if !u.mailable}
										<span class="ml-1 inline-flex items-center rounded-full bg-gray-100 px-2 py-0.5 text-xs font-normal text-gray-500" title="Unverified, unsubscribed from product updates, or bounced">no mail</span>
									{/if}
								</td>
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
								<td class="px-4 py-2 text-center">
									{#if u.user_id === currentUserId}
										<span class="text-xs text-gray-400" title="You can't delete your own account from here — use Settings → Delete account.">(you)</span>
									{:else}
										<button
											type="button"
											onclick={() => openDeleteUserModal(u)}
											title={`Delete ${u.email} and their ${u.project_count} project(s)`}
											aria-label={`Delete ${u.email}`}
											class="inline-flex items-center justify-center rounded p-1.5 text-red-600 hover:bg-red-50 transition-colors cursor-pointer"
										>
											<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.6" stroke="currentColor">
												<path stroke-linecap="round" stroke-linejoin="round" d="m14.74 9-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 0 1-2.244 2.077H8.084a2.25 2.25 0 0 1-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 0 0-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 0 1 3.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 0 0-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 0 0-7.5 0" />
											</svg>
										</button>
									{/if}
								</td>
							</tr>
						{/each}
					{/if}
				</tbody>
			</table>
		</div>
	</section>
	{/if}

	{#if activeTab === 'contact'}
	<section class="space-y-3">
		<h2 class="text-lg font-semibold text-gray-900">Contact Requests</h2>
		<p class="text-sm text-gray-600">
			Contact-form messages, Team-beta requests and Team-tier support tickets are in the
			<a href="/admin/inbox" class="text-blue-600 hover:underline">Inbox</a>, in full, with resolve / reopen and notes.
		</p>
	</section>
	{/if}

	{#if activeTab === 'projects'}
	<section class="space-y-3">
		<h2 class="text-lg font-semibold text-gray-900">All Projects</h2>
		<div class="rounded-md border border-gray-200 bg-white overflow-x-auto">
			<table class="w-full text-sm">
				<thead class="bg-gray-50 text-left text-xs uppercase text-gray-500">
					<tr>
						<th class="px-4 py-2">Project</th>
						<th class="px-4 py-2">Slug</th>
						<th class="px-4 py-2">Owner</th>
						<th class="px-4 py-2">Plan</th>
						<th class="px-4 py-2">Status</th>
						<th class="px-4 py-2">Created</th>
						<th class="px-4 py-2">Beta ends</th>
						<th class="px-4 py-2 text-right">Actions</th>
					</tr>
				</thead>
				<tbody class="divide-y divide-gray-100">
					{#if loading}
						<tr><td colspan="8" class="px-4 py-6 text-center text-gray-400">Loading…</td></tr>
					{:else if projects.length === 0}
						<tr><td colspan="8" class="px-4 py-6 text-center text-gray-400">No projects.</td></tr>
					{:else}
						{#each projects as p}
							<tr>
								<td class="px-4 py-2 font-medium text-gray-900">{p.name}</td>
								<td class="px-4 py-2 font-mono text-gray-600">{p.slug}</td>
								<td class="px-4 py-2 text-gray-600">{p.owner_email}</td>
								<td class="px-4 py-2 text-gray-600">{p.plan}</td>
								<td class="px-4 py-2">
									{#if p.status === 'suspended'}
										<span class="inline-flex rounded-full bg-red-100 px-2 py-0.5 text-xs font-semibold text-red-700">suspended</span>
									{:else}
										<span class="text-gray-600">{p.status}</span>
									{/if}
								</td>
								<td class="px-4 py-2 text-gray-500">{new Date(p.created_at).toLocaleDateString()}</td>
								<td class="px-4 py-2 whitespace-nowrap">
									{#if p.beta_ends_at}
										<span class={p.beta_expired_at ? 'font-medium text-red-700' : new Date(p.beta_ends_at).getTime() - Date.now() < 4 * 86400000 ? 'font-medium text-amber-700' : 'text-gray-600'}>
											{new Date(p.beta_ends_at).toLocaleDateString()}{p.beta_expired_at ? ' (ended)' : ''}
										</span>
									{:else}
										<span class="text-gray-300">—</span>
									{/if}
								</td>
								<td class="px-4 py-2 text-right whitespace-nowrap">
									{#if p.beta_ends_at}
										<button
											type="button"
											onclick={() => extendTeamBeta(p)}
											disabled={projectBusy !== null}
											title="Move the Team beta end 14 days later; reactivates the project if the beta end suspended it"
											class="mr-1 rounded-md border border-eurobase-200 px-2.5 py-1 text-xs font-medium text-eurobase-700 hover:bg-eurobase-50 cursor-pointer disabled:opacity-50"
										>
											{projectBusy === p.id ? 'Extending…' : 'Extend 14 days'}
										</button>
									{/if}
									{#if p.status === 'active'}
										<button
											type="button"
											onclick={() => { suspendTarget = p; suspendReason = ''; suspendError = null; }}
											disabled={projectBusy !== null}
											class="rounded-md border border-red-300 px-2.5 py-1 text-xs font-medium text-red-700 hover:bg-red-50 cursor-pointer disabled:opacity-50"
										>
											Suspend
										</button>
									{:else if p.status === 'suspended' && !p.beta_expired_at}
										<button
											type="button"
											onclick={() => unsuspendProject(p)}
											disabled={projectBusy !== null}
											class="rounded-md border border-gray-300 px-2.5 py-1 text-xs font-medium text-gray-700 hover:bg-gray-50 cursor-pointer disabled:opacity-50"
										>
											{projectBusy === p.id ? 'Unsuspending…' : 'Unsuspend'}
										</button>
									{/if}
								</td>
							</tr>
						{/each}
					{/if}
				</tbody>
			</table>
		</div>
	</section>
	{/if}
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
					<h3 class="text-base font-semibold text-gray-900">
						{composeMode === 'audience' ? 'Compose broadcast' : 'Email signup users'}
					</h3>
					<p class="text-xs text-gray-500 mt-0.5">
						{#if composeMode === 'audience'}
							{#if broadcastLoading}
								Loading audience…
							{:else if broadcastError}
								<span class="text-red-600">Audience load failed: {broadcastError}</span>
							{:else}
								{audienceRecipientCount} signed-up user{audienceRecipientCount === 1 ? '' : 's'} · one email each
							{/if}
						{:else}
							{selectedSignups.size} signup user{selectedSignups.size === 1 ? '' : 's'} selected · one email each
						{/if}
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
						onclick={() => applyTemplate(TEAM_INVITATION_TEMPLATE)}
					>
						Team beta invitation
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
					<label for="compose-subject" class="block text-xs text-gray-500 mb-1">Subject</label>
					<input
						id="compose-subject"
						type="text"
						bind:value={composeSubject}
						placeholder="You're invited to Eurobase"
						class="w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
					/>
				</div>
				<div>
					<label for="compose-body" class="block text-xs text-gray-500 mb-1">Body (HTML)</label>
					<textarea
						id="compose-body"
						bind:value={composeBody}
						rows="12"
						placeholder="<p>Hi,</p><p>…</p>"
						class="w-full rounded-md border border-gray-300 px-3 py-2 text-xs font-mono"
					></textarea>
					<p class="mt-1 text-xs text-gray-400">Paste or write HTML. Plain text is fine too — just use &lt;p&gt; tags for line breaks. A line on how to turn off product updates is added at the bottom automatically.</p>
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
					disabled={composeBusy || composeSuccess !== null || (composeMode === 'audience' && (broadcastLoading || broadcastError !== null))}
					class="rounded-md bg-eurobase-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-eurobase-700 disabled:cursor-not-allowed disabled:opacity-40 cursor-pointer"
				>
					{composeBusy ? 'Sending…' : 'Send'}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Suspend project confirm modal -->
{#if suspendTarget}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" role="dialog" aria-modal="true">
		<div class="w-full max-w-md rounded-xl bg-white shadow-xl">
			<div class="border-b border-gray-200 px-5 py-3">
				<h3 class="text-base font-semibold text-red-700">Suspend project</h3>
				<p class="mt-1 text-xs text-gray-500">The project stops serving until you unsuspend it. Nothing is deleted.</p>
			</div>
			<div class="px-5 py-4 space-y-3">
				<div class="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-900">
					<div><strong>Project:</strong> {suspendTarget.name} <span class="font-mono text-xs">({suspendTarget.slug})</span></div>
					<div class="mt-1"><strong>Owner:</strong> {suspendTarget.owner_email}</div>
					<div class="mt-1"><strong>Plan:</strong> {suspendTarget.plan}</div>
				</div>
				<p class="text-sm text-gray-700">
					Its API, sign-in, storage, functions and scheduled jobs stop, and members can't open it in the console.
					The owner's account and other projects aren't affected.
				</p>
				<label class="block text-xs font-medium text-gray-700" for="suspend-reason">Reason (audit log, optional)</label>
				<input
					id="suspend-reason"
					type="text"
					bind:value={suspendReason}
					maxlength="500"
					placeholder="e.g. abuse report"
					class="w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
					autocomplete="off"
				/>
				{#if suspendError}
					<p class="text-sm text-red-700">{suspendError}</p>
				{/if}
			</div>
			<div class="flex items-center justify-end gap-2 border-t border-gray-200 px-5 py-3">
				<button
					type="button"
					onclick={() => { suspendTarget = null; }}
					disabled={projectBusy !== null}
					class="rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-700 hover:bg-gray-50 cursor-pointer disabled:opacity-50"
				>
					Cancel
				</button>
				<button
					type="button"
					onclick={confirmSuspendProject}
					disabled={projectBusy !== null}
					class="rounded-md bg-red-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-red-700 disabled:cursor-not-allowed disabled:opacity-40 cursor-pointer"
				>
					{projectBusy ? 'Suspending…' : 'Suspend project'}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Delete user confirm modal -->
{#if deleteUserTarget}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
		<div class="w-full max-w-md rounded-xl bg-white shadow-xl">
			<div class="border-b border-gray-200 px-5 py-3">
				<h3 class="text-base font-semibold text-red-700">Delete user + all their data</h3>
				<p class="mt-1 text-xs text-gray-500">This is irreversible.</p>
			</div>
			<div class="px-5 py-4 space-y-4">
				<div class="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-900">
					<div><strong>Email:</strong> {deleteUserTarget.email}</div>
					<div class="mt-1">
						<strong>Projects:</strong> {deleteUserTarget.project_count}
						{#if deleteUserTarget.project_count > 0}
							<span class="text-xs">(each dedicated Scaleway instance will be torn down)</span>
						{/if}
					</div>
					{#if deleteUserTarget.plan === 'pro'}
						<div class="mt-1"><strong>Plan:</strong> Pro (active subscription)</div>
					{/if}
					{#if deleteUserTarget.team_beta_access || deleteUserTarget.legal_team_beta_access}
						<div class="mt-1">
							<strong>Beta access:</strong>
							{[
								deleteUserTarget.team_beta_access ? 'Team' : null,
								deleteUserTarget.legal_team_beta_access ? 'Legal Team' : null,
							].filter(Boolean).join(', ')}
						</div>
					{/if}
				</div>
				<p class="text-sm text-gray-700">
					Type the email <code class="rounded bg-gray-100 px-1 py-0.5 text-xs font-mono">{deleteUserTarget.email}</code> to confirm.
				</p>
				<input
					type="text"
					bind:value={deleteUserConfirmInput}
					placeholder="Type email to confirm"
					class="w-full rounded-md border border-gray-300 px-3 py-2 text-sm font-mono"
					autocomplete="off"
				/>
			</div>
			<div class="flex items-center justify-end gap-2 border-t border-gray-200 px-5 py-3">
				<button
					type="button"
					onclick={closeDeleteUserModal}
					disabled={deleteUserBusy}
					class="rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-700 hover:bg-gray-50 cursor-pointer disabled:opacity-50"
				>
					Cancel
				</button>
				<button
					type="button"
					onclick={confirmDeleteUser}
					disabled={deleteUserBusy ||
						deleteUserConfirmInput.trim().toLowerCase() !== deleteUserTarget.email.toLowerCase()}
					class="rounded-md bg-red-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-red-700 disabled:cursor-not-allowed disabled:opacity-40 cursor-pointer"
				>
					{deleteUserBusy ? 'Deleting…' : 'Delete permanently'}
				</button>
			</div>
		</div>
	</div>
{/if}
