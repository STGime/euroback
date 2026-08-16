<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { api, type Project } from '$lib/api.js';
	import { projects, projectsLoading, projectsError, loadProjects } from '$lib/stores.js';

	// Modal state
	let showNewModal = $state(false);
	let newName = $state('');
	let newPlan = $state('free');
	let creating = $state(false);
	let createError = $state('');
	// Team-tier closed-beta gate (M2). Populated on mount from the
	// profile endpoint; controls whether the Team option appears on
	// the plan picker.
	let hasTeamBeta = $state(false);

	// Derived slug from name
	let newSlug = $derived(
		newName
			.toLowerCase()
			.replace(/[^a-z0-9\s-]/g, '')
			.replace(/\s+/g, '-')
			.replace(/-+/g, '-')
			.replace(/^-|-$/g, '')
	);

	let redirecting = $state(false);

	// Pro-checkout return handling. Mollie redirects to
	// /projects?status=success&pending=<id> on success, ?status=
	// canceled on user-cancel, ?status=failed on payment failure.
	// sessionStorage carries the form state across the Mollie
	// roundtrip so we can restore-and-retry on cancel/fail or
	// navigate to the created project on success. See #406.
	const PENDING_KEY = 'eurobase.pending_project_checkout';
	let checkoutBanner = $state<{ kind: 'polling' | 'canceled' | 'failed' | 'error'; msg: string } | null>(null);

	function readPendingIntent(): { pendingId: string; name: string; slug: string; region: string; plan: string } | null {
		if (typeof sessionStorage === 'undefined') return null;
		const raw = sessionStorage.getItem(PENDING_KEY);
		if (!raw) return null;
		try {
			return JSON.parse(raw);
		} catch {
			sessionStorage.removeItem(PENDING_KEY);
			return null;
		}
	}

	function clearPendingIntent(): void {
		if (typeof sessionStorage !== 'undefined') {
			sessionStorage.removeItem(PENDING_KEY);
		}
	}

	// Poll listProjects for a project matching the pending slug.
	// The webhook creates the project after Mollie confirms — it
	// may lag the redirect by a few seconds. Cap attempts so a
	// webhook-delivery failure doesn't spin forever.
	async function pollForProject(slug: string, maxAttempts = 10): Promise<Project | null> {
		for (let i = 0; i < maxAttempts; i++) {
			try {
				const list = await api.listProjects();
				const found = list.find((p) => p.slug === slug);
				if (found) return found;
			} catch {
				// swallow transient list failures, keep polling
			}
			await new Promise((r) => setTimeout(r, 1500));
		}
		return null;
	}

	async function handleMollieReturn(): Promise<boolean> {
		const status = $page.url.searchParams.get('status');
		const pendingParam = $page.url.searchParams.get('pending');
		if (!status || !pendingParam) return false;

		const intent = readPendingIntent();
		// Missing sessionStorage intent = tab reopened / user cleared
		// storage / different device. We can still poll if we have
		// the pending param, but slug matching needs the slug. Fall
		// back to reloading projects and letting the user see the
		// (possibly-created) list.
		if (!intent) {
			checkoutBanner = { kind: 'error', msg: 'Payment session expired. Refreshing project list.' };
			await loadProjects();
			return true;
		}

		if (status === 'success') {
			checkoutBanner = { kind: 'polling', msg: 'Payment received — provisioning your project…' };
			const found = await pollForProject(intent.slug);
			clearPendingIntent();
			if (found) {
				goto(`/p/${found.id}`);
				return true;
			}
			checkoutBanner = {
				kind: 'error',
				msg: 'Payment went through but your project is still provisioning. It should appear in your list within a minute — contact support if not.',
			};
			await loadProjects();
			return true;
		}

		if (status === 'canceled') {
			// User closed Mollie's tab or picked cancel. sessionStorage
			// values pre-fill the modal so they can retry without
			// re-typing.
			checkoutBanner = { kind: 'canceled', msg: 'Checkout canceled. No payment was taken.' };
			showNewModal = true;
			newName = intent.name;
			newPlan = intent.plan;
			clearPendingIntent();
			return true;
		}

		if (status === 'failed') {
			checkoutBanner = {
				kind: 'failed',
				msg: 'Payment failed. Please try again with a different card, or contact your bank.',
			};
			showNewModal = true;
			newName = intent.name;
			newPlan = intent.plan;
			clearPendingIntent();
			return true;
		}
		return false;
	}

	onMount(async () => {
		// Load profile in parallel with projects so the Team option
		// is available immediately when the modal opens. Failure is
		// non-fatal — hasTeamBeta stays false.
		const [_, profile] = await Promise.all([
			loadProjects(),
			api.getProfile().catch(() => null)
		]);
		if (profile) hasTeamBeta = !!profile.team_beta_access;

		// Post-Mollie return handling. If we're landing here from a
		// Pro checkout, this either navigates away (success) or shows
		// a banner + reopens the modal (canceled/failed).
		if (await handleMollieReturn()) {
			return;
		}

		// Auto-redirect new users (0 projects) to onboarding.
		if ($projects.length === 0 && !$projectsError) {
			redirecting = true;
			goto('/onboarding');
			return;
		}

		// Deep link from pricing page: /projects?new=team opens the
		// modal pre-set to Team. Only honoured if the user actually
		// has beta access — otherwise silently ignored.
		const wantNew = $page.url.searchParams.get('new');
		if (wantNew === 'team' && hasTeamBeta) {
			openModal('team');
		}
	});

	function openModal(preselectPlan?: string) {
		newName = '';
		newPlan = preselectPlan && (preselectPlan === 'team' ? hasTeamBeta : true) ? preselectPlan : 'free';
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
			if (newPlan === 'pro') {
				// Payment-first flow (#406). Start Mollie checkout;
				// project is created by the webhook after payment.
				// sessionStorage the intent so we can restore the
				// modal on cancel/fail or navigate to the created
				// project on success.
				const intent = {
					name: newName.trim(),
					slug: newSlug,
					region: 'fr-par',
					plan: newPlan,
				};
				const res = await api.startProjectCheckout({
					name: intent.name,
					slug: intent.slug,
					region: intent.region,
					plan_code: 'pro',
				});
				sessionStorage.setItem(
					PENDING_KEY,
					JSON.stringify({ pendingId: res.pending_project_id, ...intent })
				);
				window.location.href = res.checkout_url;
				return; // don't reset creating — redirect in flight
			}

			// Free / Team — existing synchronous path.
			await api.createProject({
				name: newName.trim(),
				slug: newSlug,
				region: 'fr-par',
				plan: newPlan
			});
			showNewModal = false;
			await loadProjects();
		} catch (err) {
			createError = mapCreateError(err);
		} finally {
			creating = false;
		}
	}

	// mapCreateError translates both the classic createProject
	// error shapes and the new Pro-checkout error codes into
	// user-facing copy.
	function mapCreateError(err: unknown): string {
		const msg = err instanceof Error ? err.message : 'Failed to create project';
		if (msg.includes('slug_taken')) {
			return 'That project name is already in use — please choose another.';
		}
		if (msg.includes('pending_checkout_in_flight')) {
			return 'Another checkout is already in progress for your account. Complete it or wait a few minutes and try again.';
		}
		if (msg.includes('billing_disabled')) {
			return 'Paid plans are temporarily unavailable. Create a Free project or contact support.';
		}
		if (msg.includes('limited to') && msg.includes('project')) {
			return "You've reached the project limit on your current plan. Upgrade to Pro for more projects.";
		}
		return msg.replace(/^API \d+:\s*/, '').replace(/^\{.*"error"\s*:\s*"/, '').replace(/"\s*\}$/, '');
	}

	function statusColor(status: string): string {
		switch (status) {
			case 'active':
				return 'bg-emerald-50 text-emerald-700 ring-emerald-600/20';
			case 'provisioning':
				return 'bg-amber-50 text-amber-700 ring-amber-600/20';
			case 'suspended':
				return 'bg-red-50 text-red-700 ring-red-600/20';
			default:
				return 'bg-gray-50 text-gray-600 ring-gray-500/10';
		}
	}

	function planColor(plan: string): string {
		switch (plan) {
			case 'free':
				return 'bg-gray-50 text-gray-600 ring-gray-500/10';
			case 'pro':
				return 'bg-eurobase-50 text-eurobase-700 ring-eurobase-600/20';
			case 'enterprise':
				return 'bg-purple-50 text-purple-700 ring-purple-600/20';
			default:
				return 'bg-gray-50 text-gray-600 ring-gray-500/10';
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
	<title>Projects - Eurobase Console</title>
</svelte:head>

{#if redirecting}
	<!-- Redirecting to onboarding — show nothing -->
{:else}
<div class="mx-auto max-w-6xl">
	<!-- Mollie checkout return banner (see handleMollieReturn). -->
	{#if checkoutBanner}
		<div
			class="mb-4 rounded-md border p-4 text-sm {
				checkoutBanner.kind === 'polling' ? 'border-eurobase-200 bg-eurobase-50 text-eurobase-800'
				: checkoutBanner.kind === 'canceled' ? 'border-gray-200 bg-gray-50 text-gray-800'
				: 'border-red-200 bg-red-50 text-red-800'
			}"
		>
			<div class="flex items-center gap-2">
				{#if checkoutBanner.kind === 'polling'}
					<svg class="h-4 w-4 animate-spin text-eurobase-600" fill="none" viewBox="0 0 24 24">
						<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
						<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
					</svg>
				{/if}
				<span class="font-medium">{checkoutBanner.msg}</span>
			</div>
		</div>
	{/if}

	<!-- Page header -->
	<div class="flex items-center justify-between">
		<div>
			<h1 class="text-2xl font-bold text-gray-900">Your Projects</h1>
			<p class="mt-1 text-sm text-gray-500">Manage your EU-sovereign backend projects</p>
		</div>
		<a
			href="/onboarding"
			class="inline-flex items-center gap-2 rounded-lg bg-eurobase-600 px-4 py-2.5 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 transition-colors cursor-pointer"
		>
			<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
			</svg>
			New Project
		</a>
	</div>

	<!-- Loading state -->
	{#if $projectsLoading}
		<div class="mt-16 flex flex-col items-center text-center">
			<svg class="h-10 w-10 animate-spin text-eurobase-600" fill="none" viewBox="0 0 24 24">
				<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
				<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
			</svg>
			<p class="mt-4 text-sm text-gray-500">Loading projects...</p>
		</div>
	{:else if $projectsError}
		<!-- Error state -->
		<div class="mt-16 flex flex-col items-center text-center">
			<div class="flex h-20 w-20 items-center justify-center rounded-2xl bg-red-50">
				<svg class="h-10 w-10 text-red-400" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
				</svg>
			</div>
			<h3 class="mt-4 text-lg font-semibold text-gray-900">Failed to load projects</h3>
			<p class="mt-2 max-w-sm text-sm text-gray-500">{$projectsError}</p>
			<button
				onclick={() => loadProjects()}
				class="mt-4 rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-semibold text-white hover:bg-eurobase-700 transition-colors cursor-pointer"
			>
				Retry
			</button>
		</div>
	{:else if $projects.length === 0 && !redirecting}
		<!-- Empty state -->
		<div class="mt-16 flex flex-col items-center text-center">
			<div class="flex h-20 w-20 items-center justify-center rounded-2xl bg-gray-100">
				<svg class="h-10 w-10 text-gray-400" fill="none" viewBox="0 0 24 24" stroke-width="1" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" d="M20.25 6.375c0 2.278-3.694 4.125-8.25 4.125S3.75 8.653 3.75 6.375m16.5 0c0-2.278-3.694-4.125-8.25-4.125S3.75 4.097 3.75 6.375m16.5 0v11.25c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125V6.375m16.5 0v3.75m-16.5-3.75v3.75m16.5 0v3.75C20.25 16.153 16.556 18 12 18s-8.25-1.847-8.25-4.125v-3.75m16.5 0c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125" />
				</svg>
			</div>
			<h3 class="mt-4 text-lg font-semibold text-gray-900">No projects yet</h3>
			<p class="mt-2 max-w-sm text-sm text-gray-500">
				Create your first EU-sovereign project. Each project gets its own PostgreSQL database,
				object storage, and API endpoint — all hosted in EU datacenters.
			</p>
			<a
				href="/onboarding"
				class="mt-6 inline-flex items-center gap-2 rounded-lg bg-eurobase-600 px-5 py-2.5 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 transition-colors cursor-pointer"
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
				</svg>
				Create your first project
			</a>
		</div>
	{:else}
		<!-- Project cards grid -->
		<div class="mt-6 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
			{#each $projects as project}
				<a
					href="/p/{project.id}"
					class="group rounded-xl border border-gray-200 bg-white p-5 shadow-sm transition-all hover:border-eurobase-300 hover:shadow-md"
				>
					<div class="flex items-start justify-between">
						<div class="flex h-10 w-10 items-center justify-center rounded-lg bg-eurobase-50 text-eurobase-600 group-hover:bg-eurobase-100 transition-colors">
							<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" d="M20.25 6.375c0 2.278-3.694 4.125-8.25 4.125S3.75 8.653 3.75 6.375m16.5 0c0-2.278-3.694-4.125-8.25-4.125S3.75 4.097 3.75 6.375m16.5 0v11.25c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125V6.375m16.5 0v3.75m-16.5-3.75v3.75m16.5 0v3.75C20.25 16.153 16.556 18 12 18s-8.25-1.847-8.25-4.125v-3.75m16.5 0c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125" />
							</svg>
						</div>
						<span class="inline-flex items-center rounded-full px-2 py-1 text-xs font-medium ring-1 ring-inset {statusColor(project.status)}">
							{#if project.status === 'provisioning'}
								<svg class="mr-1 h-3 w-3 animate-spin" fill="none" viewBox="0 0 24 24">
									<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
									<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
								</svg>
							{/if}
							{project.status}
						</span>
					</div>

					<h3 class="mt-3 text-base font-semibold text-gray-900 group-hover:text-eurobase-700 transition-colors">
						{project.name}
					</h3>
					<p class="mt-0.5 text-sm text-gray-500 font-mono">
						{project.slug}.eurobase.app
					</p>

					<div class="mt-4 flex items-center gap-2 flex-wrap">
						<span class="inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset {planColor(project.plan)}">
							{project.plan}
						</span>
						<span class="inline-flex items-center gap-1 text-xs text-gray-400">
							<svg class="h-3 w-3" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" d="M15 10.5a3 3 0 1 1-6 0 3 3 0 0 1 6 0Z" />
								<path stroke-linecap="round" stroke-linejoin="round" d="M19.5 10.5c0 7.142-7.5 11.25-7.5 11.25S4.5 17.642 4.5 10.5a7.5 7.5 0 1 1 15 0Z" />
							</svg>
							{project.region}
						</span>
					</div>

					<div class="mt-3 border-t border-gray-100 pt-3">
						<span class="text-xs text-gray-400">Created {formatDate(project.created_at)}</span>
					</div>
				</a>
			{/each}
		</div>
	{/if}
</div>
{/if}

<!-- New Project Modal -->
{#if showNewModal}
	<!-- svelte-ignore a11y_click_events_have_key_events -->
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- Backdrop -->
		<div class="absolute inset-0 bg-black/50" onclick={closeModal}></div>

		<!-- Dialog -->
		<div class="relative w-full max-w-md rounded-2xl bg-white p-6 shadow-xl mx-4">
			<h2 class="text-lg font-semibold text-gray-900">Create New Project</h2>
			<p class="mt-1 text-sm text-gray-500">Each project gets its own database, storage, and API endpoint.</p>

			{#if createError}
				<div class="mt-4 rounded-lg bg-red-50 border border-red-200 p-3 text-sm text-red-700">
					{createError}
				</div>
			{/if}

			<form onsubmit={handleCreate} class="mt-5 space-y-4">
				<!-- Project Name -->
				<div>
					<label for="project-name" class="block text-sm font-medium text-gray-700">Project Name</label>
					<input
						id="project-name"
						type="text"
						bind:value={newName}
						required
						placeholder="My Awesome Project"
						class="mt-1 block w-full rounded-lg border border-gray-300 px-3.5 py-2.5 text-sm text-gray-900 shadow-sm placeholder:text-gray-400 focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
					/>
					{#if newSlug}
						<p class="mt-1 text-xs text-gray-400 font-mono">{newSlug}.eurobase.app</p>
					{/if}
				</div>

				<!-- Region (locked to Paris) -->
				<div>
					<label for="project-region" class="block text-sm font-medium text-gray-700">Region</label>
					<select
						id="project-region"
						disabled
						class="mt-1 block w-full rounded-lg border border-gray-300 bg-gray-50 px-3.5 py-2.5 text-sm text-gray-500 shadow-sm cursor-not-allowed"
					>
						<option>EU West - Paris</option>
					</select>
					<p class="mt-1 text-xs text-gray-400">Additional EU regions coming soon</p>
				</div>

				<!-- Plan -->
				<fieldset>
					<legend class="block text-sm font-medium text-gray-700">Plan for this project</legend>
					<p class="text-xs text-gray-400 mt-0.5">Each project is billed independently</p>
					<div class="mt-2 flex gap-3">
						<label class="flex-1 cursor-pointer">
							<input type="radio" name="plan" value="free" bind:group={newPlan} class="peer sr-only" />
							<div class="rounded-lg border-2 p-3 text-center transition-colors peer-checked:border-eurobase-600 peer-checked:bg-eurobase-50 border-gray-200 hover:border-gray-300">
								<p class="text-sm font-semibold text-gray-900">Free</p>
								<p class="text-xs text-gray-500">$0/mo</p>
							</div>
						</label>
						<label class="flex-1 cursor-pointer">
							<input type="radio" name="plan" value="pro" bind:group={newPlan} class="peer sr-only" />
							<div class="rounded-lg border-2 p-3 text-center transition-colors peer-checked:border-eurobase-600 peer-checked:bg-eurobase-50 border-gray-200 hover:border-gray-300">
								<p class="text-sm font-semibold text-gray-900">Pro</p>
								<p class="text-xs text-eurobase-600 font-medium">&euro;19/mo</p>
							</div>
						</label>
						{#if hasTeamBeta}
							<!-- Team-tier closed-beta option (M2). Only rendered
							     for users with team_beta_access = true; everyone
							     else keeps the two-option Free/Pro picker. -->
							<label class="flex-1 cursor-pointer">
								<input type="radio" name="plan" value="team" bind:group={newPlan} class="peer sr-only" />
								<div class="rounded-lg border-2 p-3 text-center transition-colors peer-checked:border-emerald-600 peer-checked:bg-emerald-50 border-emerald-200 hover:border-emerald-300">
									<p class="text-sm font-semibold text-gray-900">Team</p>
									<p class="text-xs text-emerald-700 font-medium">Beta · free</p>
								</div>
							</label>
						{/if}
					</div>
					{#if newPlan === 'team'}
						<p class="mt-2 text-xs text-gray-500">
							Provisions a dedicated Postgres instance in fr-par (Scaleway).
							Takes 2–5 min after project creation to reach <code class="rounded bg-gray-100 px-1 py-0.5 text-[11px]">active</code>.
						</p>
					{/if}
				</fieldset>

				<!-- Actions -->
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
							<svg class="h-4 w-4 animate-spin" fill="none" viewBox="0 0 24 24">
								<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
								<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
							</svg>
							Creating...
						{:else}
							Create Project
						{/if}
					</button>
				</div>
			</form>
		</div>
	</div>
{/if}
