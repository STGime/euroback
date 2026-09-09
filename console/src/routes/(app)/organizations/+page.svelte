<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { api, APIError, type OrgWithMembership } from '$lib/api.js';

	let orgs = $state<OrgWithMembership[]>([]);
	let loading = $state(true);
	let error = $state('');
	// Team-tier gate: creation is Team-only, but invited members of
	// a Team-org keep read access regardless of their own tier.
	let hasTeamBeta = $state<boolean>(false);
	// Both `loading` (orgs list) and `profileLoaded` must be true
	// before we render the empty-state branch. Otherwise a Team user
	// with 0 orgs briefly sees the non-Team "Upgrade to Team"
	// nudge while getProfile is still in flight.
	let profileLoaded = $state<boolean>(false);

	// New-org modal state
	let showNewModal = $state(false);
	let newName = $state('');
	let creating = $state(false);
	let createError = $state('');

	onMount(async () => {
		// Fetch profile + orgs in parallel; both are needed to render
		// the create-org affordance vs the upgrade nudge.
		const [profile] = await Promise.all([
			api.getProfile().catch(() => null),
			load(),
		]);
		if (profile) {
			hasTeamBeta = profile.team_beta_access === true;
		}
		profileLoaded = true;
	});

	async function load() {
		loading = true;
		error = '';
		try {
			const res = await api.listOrgs();
			orgs = res.orgs ?? [];
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load organizations';
		} finally {
			loading = false;
		}
	}

	function openModal() {
		newName = '';
		createError = '';
		showNewModal = true;
	}

	function closeModal() {
		showNewModal = false;
	}

	async function handleCreate(e: Event) {
		e.preventDefault();
		if (!newName.trim()) return;
		creating = true;
		createError = '';
		try {
			const org = await api.createOrg(newName.trim());
			showNewModal = false;
			await goto(`/organizations/${org.id}`);
		} catch (err) {
			if (err instanceof APIError) {
				createError = err.message;
			} else {
				createError = err instanceof Error ? err.message : 'Failed to create organization';
			}
		} finally {
			creating = false;
		}
	}

	function formatDate(dateStr: string): string {
		return new Date(dateStr).toLocaleDateString('en-GB', {
			day: 'numeric',
			month: 'short',
			year: 'numeric'
		});
	}
</script>

<svelte:head>
	<title>Organizations - Eurobase Console</title>
</svelte:head>

<div class="mx-auto max-w-6xl">
	<div class="flex items-center justify-between">
		<div>
			<h1 class="text-2xl font-bold text-gray-900">Organizations</h1>
			<p class="mt-1 text-sm text-gray-500">
				Organizations bundle projects, members, and SSO for your team.
				<span class="ml-1 rounded bg-emerald-50 px-1.5 py-0.5 text-[11px] font-medium text-emerald-700 ring-1 ring-inset ring-emerald-600/20">Team-tier</span>
			</p>
		</div>
		{#if hasTeamBeta}
			<button
				type="button"
				onclick={openModal}
				class="inline-flex items-center gap-2 rounded-lg bg-eurobase-600 px-4 py-2.5 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 transition-colors cursor-pointer"
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
				</svg>
				New Organization
			</button>
		{:else}
			<a
				href="/pricing"
				class="inline-flex items-center gap-2 rounded-lg border border-eurobase-300 bg-white px-4 py-2.5 text-sm font-semibold text-eurobase-700 shadow-sm hover:bg-eurobase-50 focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 transition-colors cursor-pointer"
				title="Organizations + SSO are a Team-tier feature"
			>
				Upgrade to Team
			</a>
		{/if}
	</div>

	{#if loading || !profileLoaded}
		<div class="mt-16 flex flex-col items-center text-center">
			<svg class="h-10 w-10 animate-spin text-eurobase-600" fill="none" viewBox="0 0 24 24">
				<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
				<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
			</svg>
			<p class="mt-4 text-sm text-gray-500">Loading organizations…</p>
		</div>
	{:else if error}
		<div class="mt-8 rounded-lg bg-red-50 border border-red-200 p-4 text-sm text-red-700">
			{error}
		</div>
	{:else if orgs.length === 0}
		<div class="mt-16 flex flex-col items-center text-center">
			<div class="flex h-20 w-20 items-center justify-center rounded-2xl bg-gray-100">
				<svg class="h-10 w-10 text-gray-400" fill="none" viewBox="0 0 24 24" stroke-width="1" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" d="M3.75 21h16.5M4.5 3h15M5.25 3v18m13.5-18v18M9 6.75h1.5m-1.5 3h1.5m-1.5 3h1.5m3-6H15m-1.5 3H15m-1.5 3H15M9 21v-3.375c0-.621.504-1.125 1.125-1.125h3.75c.621 0 1.125.504 1.125 1.125V21" />
				</svg>
			</div>
			{#if hasTeamBeta}
				<h3 class="mt-4 text-lg font-semibold text-gray-900">No organizations yet</h3>
				<p class="mt-2 max-w-sm text-sm text-gray-500">
					Create an organization to invite teammates and set up single sign-on (OIDC).
					Existing per-user projects are unaffected.
				</p>
				<button
					type="button"
					onclick={openModal}
					class="mt-6 inline-flex items-center gap-2 rounded-lg bg-eurobase-600 px-5 py-2.5 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 transition-colors cursor-pointer"
				>
					<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
					</svg>
					Create your first organization
				</button>
			{:else}
				<h3 class="mt-4 text-lg font-semibold text-gray-900">Organizations are a Team-tier feature</h3>
				<p class="mt-2 max-w-md text-sm text-gray-500">
					Upgrade to Team to create organizations, invite teammates, and enable
					single sign-on (OIDC) for your work IdP. If someone invites you to their
					organization, it will appear here automatically.
				</p>
				<a
					href="/pricing"
					class="mt-6 inline-flex items-center gap-2 rounded-lg bg-eurobase-600 px-5 py-2.5 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 transition-colors cursor-pointer"
				>
					See Team-tier pricing
				</a>
			{/if}
		</div>
	{:else}
		<div class="mt-6 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
			{#each orgs as org}
				<a
					href="/organizations/{org.id}"
					class="group rounded-xl border border-gray-200 bg-white p-5 shadow-sm transition-all hover:border-eurobase-300 hover:shadow-md"
				>
					<div class="flex items-start justify-between">
						<div class="flex h-10 w-10 items-center justify-center rounded-lg bg-eurobase-50 text-eurobase-600 group-hover:bg-eurobase-100 transition-colors">
							<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" d="M3.75 21h16.5M4.5 3h15M5.25 3v18m13.5-18v18M9 6.75h1.5m-1.5 3h1.5m-1.5 3h1.5m3-6H15m-1.5 3H15m-1.5 3H15M9 21v-3.375c0-.621.504-1.125 1.125-1.125h3.75c.621 0 1.125.504 1.125 1.125V21" />
							</svg>
						</div>
						<span class="inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset {org.role === 'admin' ? 'bg-eurobase-50 text-eurobase-700 ring-eurobase-600/20' : 'bg-gray-50 text-gray-600 ring-gray-500/10'}">
							{org.role}
						</span>
					</div>
					<h3 class="mt-3 text-base font-semibold text-gray-900 group-hover:text-eurobase-700 transition-colors">
						{org.name}
					</h3>
					<div class="mt-3 flex items-center gap-2 text-xs text-gray-500">
						{#if org.oidc_configured}
							<span class="inline-flex items-center gap-1 rounded-full bg-emerald-50 px-2 py-0.5 text-[11px] font-medium text-emerald-700 ring-1 ring-inset ring-emerald-600/20">
								<svg class="h-3 w-3" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
									<path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" />
								</svg>
								SSO configured
							</span>
						{:else}
							<span class="text-[11px] text-gray-400">SSO not configured</span>
						{/if}
					</div>
					<div class="mt-3 border-t border-gray-100 pt-3">
						<span class="text-xs text-gray-400">Member since {formatDate(org.member_since)}</span>
					</div>
				</a>
			{/each}
		</div>
	{/if}
</div>

{#if showNewModal}
	<!-- svelte-ignore a11y_click_events_have_key_events -->
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<div class="absolute inset-0 bg-black/50" onclick={closeModal}></div>
		<div class="relative w-full max-w-md rounded-2xl bg-white p-6 shadow-xl mx-4">
			<h2 class="text-lg font-semibold text-gray-900">Create Organization</h2>
			<p class="mt-1 text-sm text-gray-500">You'll be the first admin. Invite teammates after creation.</p>

			{#if createError}
				<div class="mt-4 rounded-lg bg-red-50 border border-red-200 p-3 text-sm text-red-700">
					{createError}
				</div>
			{/if}

			<form onsubmit={handleCreate} class="mt-5 space-y-4">
				<div>
					<label for="org-name" class="block text-sm font-medium text-gray-700">Organization Name</label>
					<input
						id="org-name"
						type="text"
						bind:value={newName}
						required
						maxlength="120"
						placeholder="Acme Corp"
						class="mt-1 block w-full rounded-lg border border-gray-300 px-3.5 py-2.5 text-sm text-gray-900 shadow-sm placeholder:text-gray-400 focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
					/>
				</div>

				<div class="flex justify-end gap-3 pt-2">
					<button
						type="button"
						onclick={closeModal}
						class="rounded-lg border border-gray-300 px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors cursor-pointer"
					>
						Cancel
					</button>
					<button
						type="submit"
						disabled={creating || !newName.trim()}
						class="inline-flex items-center gap-2 rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 transition-colors disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
					>
						{#if creating}
							Creating…
						{:else}
							Create
						{/if}
					</button>
				</div>
			</form>
		</div>
	</div>
{/if}
