<script lang="ts">
	import { goto } from '$app/navigation';
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { api, APIError, type Project, type AuthConfig, type PlanLimits } from '$lib/api.js';
	import { loadProjects } from '$lib/stores.js';

	// State
	let projectName = $state('');
	let plan = $state('free');
	let creating = $state(false);
	let createError = $state('');
	let planData = $state<PlanLimits[]>([]);

	// Team-tier closed-beta gate (M2). Populated on mount from the
	// profile endpoint; controls whether the Team option appears on
	// this first-project picker. Mirrors the pattern on
	// routes/(app)/projects/+page.svelte so a user granted
	// team_beta_access sees the Team option on BOTH create paths
	// (onboarding + subsequent "New Project" modal). Without this,
	// a granted user's first project could only be Free/Pro and
	// they'd have to create-then-upgrade — reported by the founder
	// after grant-then-onboarding didn't show Team.
	let hasTeamBeta = $state(false);
	// Legal-Team closed-beta gate. Symmetric with hasTeamBeta —
	// populated from profile.legal_team_beta_access. Backend
	// CreateProject enforces the same gate (ErrLegalTeamBetaRequired
	// → 403 legal_team_beta_required), so this control just decides
	// UI visibility; a user without access sending the plan directly
	// still gets a clean 403.
	let hasLegalTeamBeta = $state(false);

	onMount(async () => {
		try {
			const [plans, profile] = await Promise.all([
				api.getPlans(),
				api.getProfile().catch(() => null),
			]);
			planData = plans;
			if (profile) {
				hasTeamBeta = !!profile.team_beta_access;
				hasLegalTeamBeta = !!profile.legal_team_beta_access;
			}
		} catch {
			// Fallbacks handle the empty/errored cases — cards show
			// hardcoded copy, Team stays hidden if profile failed.
		}

		// Resume-from-profile-form: /billing/profile?next=
		// /onboarding?resume_checkout=1 lands here after the user
		// saved the billing details we needed for the Pro checkout.
		// Restore the intent from sessionStorage, re-populate the
		// wizard fields, then trigger handleCreate() to continue
		// straight to Mollie — user sees no extra click.
		const resumeCheckout = $page.url.searchParams.get('resume_checkout');
		if (resumeCheckout === '1') {
			const raw = sessionStorage.getItem(PROFILE_RESUME_KEY);
			if (raw) {
				try {
					const intent = JSON.parse(raw) as {
						name: string; slug: string; region: string; plan: string;
					};
					projectName = intent.name;
					plan = intent.plan;
					sessionStorage.removeItem(PROFILE_RESUME_KEY);
					if (typeof window !== 'undefined') {
						const url = new URL(window.location.href);
						url.searchParams.delete('resume_checkout');
						window.history.replaceState({}, '', url.toString());
					}
					// Fire the checkout on the next tick so the state
					// mutations above are applied before handleCreate
					// reads them.
					queueMicrotask(() => void handleCreate());
					return;
				} catch {
					// Fall through to the normal create step.
					sessionStorage.removeItem(PROFILE_RESUME_KEY);
				}
			}
		}

		// Resume-from-payment: /projects Mollie return handler
		// navigates here with ?resume=<projectId> when the intent
		// carried returnTo='/onboarding' (Pro checkout started
		// from the wizard). Load that project, jump to step 2 so
		// the wizard continues as if the create had been
		// synchronous. Failure silently falls through to the
		// normal create step — user can start fresh from there.
		const resumeId = $page.url.searchParams.get('resume');
		if (resumeId) {
			try {
				const project = await api.getProject(resumeId);
				createdProject = project;
				projectName = project.name;
				plan = project.plan;
				// Resumed Pro path: gate the key-display panels on
				// success step, since we can't retrieve the
				// server-generated keys. See isResumedPro doc
				// comment.
				isResumedPro = project.plan === 'pro';
				step = 'auth';
				// Tidy the URL — ?resume is a one-shot handoff, no
				// value in leaving it in the address bar after the
				// project is loaded.
				if (typeof window !== 'undefined') {
					const url = new URL(window.location.href);
					url.searchParams.delete('resume');
					window.history.replaceState({}, '', url.toString());
				}
			} catch {
				// project fetch failed — either wrong ID or a race
				// with a slow webhook. Land on the normal create
				// step and let user try again.
			}
		}
	});

	let freePlan = $derived(planData.find(p => p.plan === 'free'));
	let proPlan = $derived(planData.find(p => p.plan === 'pro'));
	let teamPlan = $derived(planData.find(p => p.plan === 'team'));
	let legalTeamPlan = $derived(planData.find(p => p.plan === 'legal_team'));

	// 4 visible tiers → 2×2 grid; 3 → 3-col; ≤2 → 2-col. Keeps
	// each card wide enough to fit its bullet list without cramping.
	let planGridClass = $derived(
		hasTeamBeta && hasLegalTeamBeta
			? 'grid-cols-1 sm:grid-cols-2 lg:grid-cols-2'
			: (hasTeamBeta || hasLegalTeamBeta)
				? 'grid-cols-1 sm:grid-cols-2 lg:grid-cols-3'
				: 'grid-cols-1 sm:grid-cols-2 lg:grid-cols-2'
	);

	function formatLimit(mb: number): string {
		if (mb >= 1024) return (mb / 1024).toFixed(0) + ' GB';
		return mb + ' MB';
	}
	let createdProject = $state<Project | null>(null);
	let step = $state<'create' | 'auth' | 'success'>('create');

	// Resumed-Pro projects can't display API keys on the success
	// step: the webhook created the project server-side and
	// discarded the plaintext keys, so `createdProject.public_key`
	// / `secret_key` are undefined on a fetched-back-later load
	// (getProject uses the list endpoint, which doesn't include
	// keys anyway — only the CreateProject response ever carries
	// them, and the webhook never returned that response to a
	// caller). Rather than render a blank "here are your keys"
	// panel, we branch on this flag to render an honest "keys
	// live on the API tab, regenerate to reveal" panel.
	// See #410 review 🟡.
	let isResumedPro = $state(false);

	// Auth config state (Step 2)
	let emailPasswordEnabled = $state(true);
	let magicLinkEnabled = $state(false);
	let phoneEnabled = $state(false);
	let requireEmailConfirmation = $state(false);
	let passwordMinLength = $state(8);
	let sessionDuration = $state('168h');
	let redirectUrls = $state('http://localhost:3000');
	let savingAuth = $state(false);
	let authError = $state('');

	// Post-creation UI
	let activeTab = $state<'quickstart' | 'curl' | 'ide'>('quickstart');
	let showSecretKey = $state(false);
	let copiedField = $state('');

	// Derived
	let slug = $derived(
		projectName
			.toLowerCase()
			.replace(/[^a-z0-9\s-]/g, '')
			.replace(/\s+/g, '-')
			.replace(/-+/g, '-')
			.replace(/^-|-$/g, '')
	);

	let publicKey = $derived(createdProject?.public_key ?? '');
	let secretKey = $derived(createdProject?.secret_key ?? '');
	let projectSlug = $derived(createdProject?.slug ?? slug);
	let projectId = $derived(createdProject?.id ?? '');
	let apiUrl = $derived(createdProject?.api_url ?? `https://${slug}.eurobase.app`);

	const sessionOptions = [
		{ value: '1h', label: '1 hour' },
		{ value: '24h', label: '24 hours' },
		{ value: '168h', label: '7 days' },
		{ value: '720h', label: '30 days' }
	];

	// Shared sessionStorage key with /projects (the Mollie return
	// handler lives on /projects and reads this to resolve the
	// created project after payment). See #406.
	const PENDING_KEY = 'eurobase.pending_project_checkout';

	// Separate key for the BEFORE-Mollie profile-form round trip
	// (PR #443). Distinct from PENDING_KEY so a post-payment resume
	// and a post-profile-form resume can't collide: PENDING_KEY
	// carries a real Mollie pending_project_id, this one carries
	// only the intent to be reused when the user comes back from
	// filling in their billing details.
	const PROFILE_RESUME_KEY = 'eurobase.onboarding_pending_checkout';

	async function handleCreate() {
		if (!projectName.trim()) return;
		creating = true;
		createError = '';
		try {
			if (plan === 'pro') {
				// Payment-first flow (#406). Stash returnTo so the
				// /projects Mollie return handler navigates BACK to
				// the wizard instead of dashboard — user continues
				// with step 2 (auth config) as if the project had
				// been created synchronously. See the ?resume=<id>
				// branch in onMount below for the pickup.
				const intent = {
					name: projectName.trim(),
					slug: slug,
					region: 'fr-par',
					plan: plan,
					returnTo: '/onboarding',
				};
				// Client-side gate: no billing profile ⇒ persist the
				// intent under PROFILE_RESUME_KEY and bounce to the
				// form. Backend also enforces (409 → catch below); the
				// two together are belt-and-braces against a stale
				// tab. Distinct from PENDING_KEY so a post-payment
				// resume and this post-profile-form resume can't
				// collide (see key comments above).
				const profile = await api.getBillingProfile();
				if (!profile) {
					sessionStorage.setItem(PROFILE_RESUME_KEY, JSON.stringify(intent));
					await goto('/billing/profile?next=' + encodeURIComponent('/onboarding?resume_checkout=1'));
					return;
				}
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
				return; // redirect in flight — don't reset `creating`
			}

			// Free / Team / Legal Team — synchronous 3-step wizard.
			// Both Team and Legal Team are Beta·free during closed
			// beta (price_cents NULL on both plan_limits rows), so
			// no Mollie checkout. When Legal Team ships paid, mirror
			// the pro branch (plan_code:'legal_team').
			const project = await api.createProject({
				name: projectName.trim(),
				slug: slug,
				region: 'fr-par',
				plan: plan
			});
			createdProject = project;
			await loadProjects();
			step = 'auth';
		} catch (err) {
			createError = mapCreateError(err);
		} finally {
			creating = false;
		}
	}

	// mapCreateError translates both classic createProject errors
	// and the new Pro-checkout error codes into user-facing copy.
	// Branches on APIError.code (machine-readable field from the
	// standard {"error", "code"} envelope) rather than
	// substring-matching err.message. Free/Team slug-clash still
	// auto-renames via applyAutoRenameOnSlugClash — kept as a
	// separate side-effecting helper so this mapper stays pure.
	// Pro slug-clash returns 409 slug_taken and shows explicit
	// copy rather than silently mutating a name after payment
	// intent.
	function mapCreateError(err: unknown): string {
		if (err instanceof APIError) {
			switch (err.code) {
				case 'slug_taken':
					return 'That project name is already in use — please choose another.';
				case 'pending_checkout_in_flight':
					return 'Another checkout is already in progress for your account. Complete it or wait a few minutes and try again.';
				case 'billing_disabled':
					return 'Paid plans are temporarily unavailable. Create a Free project or contact support.';
				case 'billing_profile_required':
					// Unreachable via the click handler (which gates on
					// getBillingProfile first). Kept as a race backstop.
					return 'Add your billing details first, then try again.';
			}
		}
		const msg = err instanceof Error ? err.message : 'Failed to create project';
		// Free/Team slug clash — no code today (tenant handler
		// doesn't set one on the 23505 branch), so fall back to
		// substring match on the message.
		if (msg.includes('409') || msg.includes('already taken')) {
			applyAutoRenameOnSlugClash();
			return `That project URL was taken. We've updated the name — click Create Project to try again, or edit it.`;
		}
		if (msg.includes('limited to') && msg.includes('project')) {
			const limit = freePlan?.project_limit ?? 2;
			return `You've reached the maximum of ${limit} projects on the Free plan. Upgrade to Pro to create up to ${proPlan?.project_limit ?? 10} projects.`;
		}
		return msg;
	}

	// applyAutoRenameOnSlugClash appends a random 4-char suffix
	// to projectName so a re-submit doesn't hit the same 23505.
	// Side-effecting on purpose — extracted from mapCreateError so
	// the mapper itself stays pure (a mapper named like a mapper
	// shouldn't quietly mutate form state).
	function applyAutoRenameOnSlugClash(): void {
		const suffix = Math.random().toString(36).slice(2, 6);
		projectName = projectName.trim() + '-' + suffix;
	}

	async function handleSaveAuthConfig() {
		if (!createdProject) return;
		savingAuth = true;
		authError = '';
		try {
			const config: AuthConfig = {
				providers: { email_password: { enabled: emailPasswordEnabled }, magic_link: { enabled: magicLinkEnabled }, phone: { enabled: phoneEnabled } },
				password_min_length: passwordMinLength,
				require_email_confirmation: requireEmailConfirmation,
				session_duration: sessionDuration,
				redirect_urls: redirectUrls.split('\n').map(u => u.trim()).filter(Boolean)
			};
			await api.updateProject(createdProject.id, { auth_config: config });
			step = 'success';
		} catch (err) {
			authError = err instanceof Error ? err.message : 'Failed to save auth config';
		} finally {
			savingAuth = false;
		}
	}

	function handleSkipAuth() {
		step = 'success';
	}

	function goToDashboard() {
		if (createdProject) {
			goto(`/p/${createdProject.id}`);
		} else {
			goto('/projects');
		}
	}

	async function copyToClipboard(text: string, field: string) {
		try {
			await navigator.clipboard.writeText(text);
			copiedField = field;
			setTimeout(() => { copiedField = ''; }, 2000);
		} catch {
			// Fallback — silently fail
		}
	}

	const sdkPkg = '@eurobase/sdk';
	let quickStartCode = $derived(`import { createClient } from '${sdkPkg}'

const eb = createClient({
  url: '${apiUrl}',
  apiKey: '${publicKey}'
})

const { data } = await eb.db.from('todos').select('*')
console.log(data)
// => [
//   { id: "...", title: "Learn about Eurobase", completed: true },
//   { id: "...", title: "Build my first EU-sovereign app", completed: false },
//   { id: "...", title: "Deploy to production", completed: false }
// ]`);

	let curlCommand = $derived(`curl -s '${apiUrl}/v1/db/todos' \\
  -H 'Authorization: Bearer ${publicKey}' | jq .`);

	let envTemplate = $derived(`EUROBASE_URL=${apiUrl}
EUROBASE_PUBLIC_KEY=${publicKey}
EUROBASE_SECRET_KEY=${secretKey}`);

	let stepNumber = $derived(step === 'create' ? 1 : step === 'auth' ? 2 : 3);
	let stepLabel = $derived(step === 'create' ? 'Create' : step === 'auth' ? 'Authentication' : 'Get Started');
</script>

<svelte:head>
	<title>Create your first project - Eurobase Console</title>
</svelte:head>

<div class="mx-auto max-w-3xl">
	<!-- Step indicator -->
	<div class="mb-6 text-center">
		<p class="text-xs font-medium text-gray-400 uppercase tracking-wider">Step {stepNumber} of 3 &middot; {stepLabel}</p>
		<div class="mt-2 flex justify-center gap-2">
			{#each [1, 2, 3] as s}
				<div class="h-1 w-16 rounded-full transition-colors {s <= stepNumber ? 'bg-eurobase-600' : 'bg-gray-200'}"></div>
			{/each}
		</div>
	</div>

	{#if step === 'create'}
		<!-- STEP 1: CREATE PROJECT -->
		<div>
			<h1 class="text-2xl font-bold text-gray-900">Create your project</h1>
			<p class="mt-2 text-sm text-gray-500 leading-relaxed">
				A project is your backend — database, file storage, and API — all hosted in the EU.
			</p>

			{#if createError}
				<div class="mt-5 rounded-lg bg-red-50 border border-red-200 p-3.5 text-sm text-red-700 flex items-start gap-2">
					<svg class="h-4 w-4 mt-0.5 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
					</svg>
					{createError}
				</div>
			{/if}

			<div class="mt-6 space-y-5">
				<!-- Project Name -->
				<div>
					<label for="onb-name" class="block text-sm font-medium text-gray-700">Project name</label>
					<input
						id="onb-name"
						type="text"
						bind:value={projectName}
						placeholder="My Awesome App"
						class="mt-1.5 block w-full rounded-lg border border-gray-300 px-3.5 py-2.5 text-sm text-gray-900 shadow-sm placeholder:text-gray-400 focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
					/>
					{#if slug}
						<p class="mt-1.5 text-xs text-gray-400 font-mono">{slug}.eurobase.app</p>
					{/if}
				</div>

				<!-- Region selector -->
				<div>
					<label for="onb-region" class="block text-sm font-medium text-gray-700">Region</label>
					<div class="relative mt-1.5">
						<select
							id="onb-region"
							disabled
							class="block w-full appearance-none rounded-lg border border-gray-300 bg-gray-50 px-3.5 py-2.5 pl-9 text-sm text-gray-500 shadow-sm cursor-not-allowed"
						>
							<option>EU West -- Paris, France</option>
						</select>
						<span class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-base" aria-hidden="true">
							&#127466;&#127482;
						</span>
					</div>
					<p class="mt-1 text-xs text-gray-400">Additional EU regions coming soon</p>
				</div>

				<!-- Plan selector -->
				<fieldset>
					<legend class="block text-sm font-medium text-gray-700">Plan for this project</legend>
					<p class="text-xs text-gray-400 mt-0.5">Each project is billed independently. You can mix Free and Pro projects. <a href="/pricing" class="text-eurobase-600 hover:text-eurobase-700 underline">See full comparison</a></p>
					<div class="mt-2 grid gap-3 {planGridClass}">
						<label class="cursor-pointer">
							<input type="radio" name="onb-plan" value="free" bind:group={plan} class="peer sr-only" />
							<div class="rounded-xl border-2 p-4 transition-all peer-checked:border-eurobase-600 peer-checked:bg-eurobase-50/50 peer-checked:shadow-sm border-gray-200 hover:border-gray-300">
								<div class="flex items-center justify-between">
									<p class="text-sm font-semibold text-gray-900">Free</p>
									<span class="text-xs font-medium text-gray-400">$0/mo</span>
								</div>
								<p class="mt-1.5 text-xs text-gray-500">Personal, learning &amp; development. Non-commercial only.</p>
								<ul class="mt-2.5 space-y-1 text-xs text-gray-500">
									<li class="flex items-center gap-1.5">
										<svg class="h-3.5 w-3.5 text-gray-400 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
										{freePlan ? formatLimit(freePlan.db_size_mb) : '500 MB'} database, {freePlan ? formatLimit(freePlan.storage_mb) : '1 GB'} file storage
									</li>
									<li class="flex items-center gap-1.5">
										<svg class="h-3.5 w-3.5 text-gray-400 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
										{freePlan ? (freePlan.mau_limit / 1000).toFixed(0) + 'k' : '10k'} auth users
									</li>
									<li class="flex items-center gap-1.5">
										<svg class="h-3.5 w-3.5 text-gray-400 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
										100 concurrent realtime connections
									</li>
									<li class="flex items-center gap-1.5">
										<svg class="h-3.5 w-3.5 text-gray-400 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
										3 edge functions, 2 cron jobs, 3 webhooks
									</li>
									<li class="flex items-center gap-1.5">
										<svg class="h-3.5 w-3.5 text-gray-400 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
										24h log retention
									</li>
								</ul>
							</div>
						</label>
						<label class="cursor-pointer">
							<input type="radio" name="onb-plan" value="pro" bind:group={plan} class="peer sr-only" />
							<div class="rounded-xl border-2 p-4 transition-all peer-checked:border-eurobase-600 peer-checked:bg-eurobase-50/50 peer-checked:shadow-sm border-gray-200 hover:border-gray-300">
								<div class="flex items-center justify-between">
									<p class="text-sm font-semibold text-gray-900">Pro</p>
									<span class="text-sm font-semibold text-eurobase-700">&euro;25/mo per project</span>
								</div>
								<p class="mt-1.5 text-xs text-gray-500">Commercial use — production apps, agencies, internal tools.</p>
								<ul class="mt-2.5 space-y-1 text-xs text-gray-500">
									<li class="flex items-center gap-1.5">
										<svg class="h-3.5 w-3.5 text-eurobase-500 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
										{proPlan ? formatLimit(proPlan.db_size_mb) : '5 GB'} database, {proPlan ? formatLimit(proPlan.storage_mb) : '50 GB'} file storage
									</li>
									<li class="flex items-center gap-1.5">
										<svg class="h-3.5 w-3.5 text-eurobase-500 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
										{proPlan ? (proPlan.mau_limit / 1000).toFixed(0) + 'k' : '100k'} auth users
									</li>
									<li class="flex items-center gap-1.5">
										<svg class="h-3.5 w-3.5 text-eurobase-500 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
										10,000 concurrent realtime connections
									</li>
									<li class="flex items-center gap-1.5">
										<svg class="h-3.5 w-3.5 text-eurobase-500 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
										25 edge functions, unlimited cron &amp; webhooks
									</li>
									<li class="flex items-center gap-1.5">
										<svg class="h-3.5 w-3.5 text-eurobase-500 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
										30-day log retention, custom email templates
									</li>
								</ul>
							</div>
						</label>
						{#if hasTeamBeta}
							<!-- Team-tier closed-beta option (M2). Only rendered
							     for users with team_beta_access = true. Emerald
							     styling matches the projects/+page.svelte New
							     Project modal so the two create paths look the
							     same to a granted user.

							     Hardcoded fallback numbers (100 GB / 500 GB /
							     1000k) are intentional — getPlans() returns
							     free/pro only for non-beta callers, so teamPlan
							     may be undefined here even when Team is
							     visible. Kept in sync with plan_limits' team
							     row (migration 000085) by hand; the values
							     will only diverge on a plan_limits change,
							     which is rare and would be caught in the same
							     PR by anyone reviewing changes to this
							     section. -->
							<label class="cursor-pointer">
								<input type="radio" name="onb-plan" value="team" bind:group={plan} class="peer sr-only" />
								<div class="rounded-xl border-2 p-4 transition-all peer-checked:border-emerald-600 peer-checked:bg-emerald-50/50 peer-checked:shadow-sm border-emerald-200 hover:border-emerald-300">
									<div class="flex items-center justify-between">
										<p class="text-sm font-semibold text-gray-900">Team</p>
										<span class="text-xs font-semibold text-emerald-700">Beta · free</span>
									</div>
									<p class="mt-1.5 text-xs text-gray-500">Dedicated Postgres, direct <code class="rounded bg-gray-100 px-1 text-[10px]">DATABASE_URL</code>, PITR + backups.</p>
									<ul class="mt-2.5 space-y-1 text-xs text-gray-500">
										<li class="flex items-center gap-1.5">
											<svg class="h-3.5 w-3.5 text-emerald-500 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
											{teamPlan ? formatLimit(teamPlan.db_size_mb) : '100 GB'} dedicated database, {teamPlan ? formatLimit(teamPlan.storage_mb) : '500 GB'} file storage
										</li>
										<li class="flex items-center gap-1.5">
											<svg class="h-3.5 w-3.5 text-emerald-500 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
											{teamPlan ? (teamPlan.mau_limit / 1000).toFixed(0) + 'k' : '1000k'} auth users
										</li>
										<li class="flex items-center gap-1.5">
											<svg class="h-3.5 w-3.5 text-emerald-500 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
											Direct <code class="rounded bg-gray-100 px-1 text-[10px]">postgres://</code> URL — Payload, Prisma, Drizzle, psql
										</li>
										<li class="flex items-center gap-1.5">
											<svg class="h-3.5 w-3.5 text-emerald-500 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
											PITR (7d), scheduled backups (30d retention)
										</li>
										<li class="flex items-center gap-1.5">
											<svg class="h-3.5 w-3.5 text-emerald-500 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
											Provisioning takes 2–5 min after Create
										</li>
									</ul>
								</div>
							</label>
						{/if}
						{#if hasLegalTeamBeta}
							<!-- Legal-Team closed-beta option. Separate SKU
							     from Team — same dedicated-Postgres infra
							     (per migration 000087, price_cents NULL
							     during beta = Beta·free treatment) plus
							     German legal-tech compliance features
							     (WORM S3 Object Lock, §257 HGB / §50 BRAO
							     / §147 AO retention, 10y audit log).

							     Amber styling matches the /pricing and
							     /legal pages so a granted user recognises
							     the same tier across surfaces. Fallback
							     numbers mirror the team plan's numeric
							     caps (both provision dedicated Postgres). -->
							<label class="cursor-pointer">
								<input type="radio" name="onb-plan" value="legal_team" bind:group={plan} class="peer sr-only" />
								<div class="rounded-xl border-2 p-4 transition-all peer-checked:border-amber-600 peer-checked:bg-amber-50/50 peer-checked:shadow-sm border-amber-200 hover:border-amber-300">
									<div class="flex items-center justify-between">
										<p class="text-sm font-semibold text-gray-900">Legal Team</p>
										<span class="text-xs font-semibold text-amber-700">Beta · free</span>
									</div>
									<p class="mt-1.5 text-xs text-gray-500">Dedicated Postgres + German legal-tech retention (§257 HGB / §50 BRAO / §147 AO).</p>
									<ul class="mt-2.5 space-y-1 text-xs text-gray-500">
										<li class="flex items-center gap-1.5">
											<svg class="h-3.5 w-3.5 text-amber-500 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
											{legalTeamPlan ? formatLimit(legalTeamPlan.db_size_mb) : '100 GB'} dedicated database, {legalTeamPlan ? formatLimit(legalTeamPlan.storage_mb) : '500 GB'} file storage
										</li>
										<li class="flex items-center gap-1.5">
											<svg class="h-3.5 w-3.5 text-amber-500 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
											WORM per-prefix retention + ad-hoc holds
										</li>
										<li class="flex items-center gap-1.5">
											<svg class="h-3.5 w-3.5 text-amber-500 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
											10-year audit log retention
										</li>
										<li class="flex items-center gap-1.5">
											<svg class="h-3.5 w-3.5 text-amber-500 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
											PITR (7d), scheduled backups (30d retention)
										</li>
										<li class="flex items-center gap-1.5">
											<svg class="h-3.5 w-3.5 text-amber-500 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
											DSAR erasure respects legal holds
										</li>
									</ul>
								</div>
							</label>
						{/if}
					</div>
				</fieldset>
			</div>

			<!-- Create button -->
			<div class="mt-8">
				<button
					onclick={handleCreate}
					disabled={creating || !projectName.trim()}
					class="inline-flex w-full items-center justify-center gap-2 rounded-lg bg-eurobase-600 px-5 py-3 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 transition-colors disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
				>
					{#if creating}
						<svg class="h-4 w-4 animate-spin" fill="none" viewBox="0 0 24 24">
							<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
							<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
						</svg>
						Creating project...
					{:else}
						Create Project
					{/if}
				</button>
			</div>
		</div>

	{:else if step === 'auth'}
		<!-- STEP 2: AUTH CONFIGURATION -->
		<div>
			<h1 class="text-2xl font-bold text-gray-900">Configure Authentication</h1>
			<p class="mt-2 text-sm text-gray-500 leading-relaxed">
				Choose how your users will sign in. You can change this later in Settings.
			</p>

			{#if authError}
				<div class="mt-5 rounded-lg bg-red-50 border border-red-200 p-3.5 text-sm text-red-700 flex items-start gap-2">
					<svg class="h-4 w-4 mt-0.5 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
					</svg>
					{authError}
				</div>
			{/if}

			<div class="mt-6 space-y-6">
				<!-- Auth Methods -->
				<div>
					<h3 class="text-sm font-semibold text-gray-900">Auth Methods</h3>
					<div class="mt-3 space-y-3">
						<!-- Email + Password -->
						<div class="flex items-center justify-between rounded-lg border border-gray-200 px-4 py-3">
							<div>
								<p class="text-sm font-medium text-gray-900">Email + Password</p>
								<p class="text-xs text-gray-500">Users sign in with email and password</p>
							</div>
							<button
								type="button"
								role="switch"
								aria-checked={emailPasswordEnabled}
								onclick={() => emailPasswordEnabled = !emailPasswordEnabled}
								class="relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 {emailPasswordEnabled ? 'bg-eurobase-600' : 'bg-gray-200'}"
							>
								<span class="pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out {emailPasswordEnabled ? 'translate-x-5' : 'translate-x-0'}"></span>
							</button>
						</div>

						<!-- Magic Links -->
						<div class="flex items-center justify-between rounded-lg border border-gray-200 px-4 py-3">
							<div>
								<p class="text-sm font-medium text-gray-900">Magic Links</p>
								<p class="text-xs text-gray-500">Passwordless sign-in via email link</p>
							</div>
							<button
								type="button"
								role="switch"
								aria-checked={magicLinkEnabled}
								onclick={() => magicLinkEnabled = !magicLinkEnabled}
								class="relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 {magicLinkEnabled ? 'bg-eurobase-600' : 'bg-gray-200'}"
							>
								<span class="pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out {magicLinkEnabled ? 'translate-x-5' : 'translate-x-0'}"></span>
							</button>
						</div>

						<!-- Phone (SMS OTP) -->
						<div class="flex items-center justify-between rounded-lg border border-gray-200 px-4 py-3">
							<div>
								<p class="text-sm font-medium text-gray-900">Phone (SMS OTP)</p>
								<p class="text-xs text-gray-500">Sign in with phone number via SMS code</p>
							</div>
							<button
								type="button"
								role="switch"
								aria-checked={phoneEnabled}
								onclick={() => phoneEnabled = !phoneEnabled}
								class="relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 {phoneEnabled ? 'bg-eurobase-600' : 'bg-gray-200'}"
							>
								<span class="pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out {phoneEnabled ? 'translate-x-5' : 'translate-x-0'}"></span>
							</button>
						</div>

						<!-- Passkeys (coming soon) -->
						<div class="flex items-center justify-between rounded-lg border border-gray-200 px-4 py-3 opacity-50 cursor-not-allowed">
							<div>
								<p class="text-sm font-medium text-gray-900">Passkeys</p>
								<p class="text-xs text-gray-500">Passwordless auth with WebAuthn</p>
							</div>
							<span class="inline-flex items-center rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-500">Coming soon</span>
						</div>

						<!-- Social Login -->
						<div class="rounded-lg border border-gray-200 px-4 py-3">
							<div>
								<p class="text-sm font-medium text-gray-900">Social Login (Google, GitHub, LinkedIn, Apple)</p>
								<p class="text-xs text-gray-500">Configure OAuth providers after project creation in Auth settings</p>
							</div>
						</div>
					</div>
				</div>

				<!-- Settings -->
				<div>
					<h3 class="text-sm font-semibold text-gray-900">Settings</h3>
					<div class="mt-3 space-y-4">
						<!-- Require email confirmation -->
						<div class="flex items-start justify-between">
							<div>
								<p class="text-sm font-medium text-gray-700">Require email confirmation</p>
								<p class="text-xs text-gray-400 mt-0.5">Email sending not yet configured</p>
							</div>
							<button
								type="button"
								role="switch"
								aria-checked={requireEmailConfirmation}
								onclick={() => requireEmailConfirmation = !requireEmailConfirmation}
								class="relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 {requireEmailConfirmation ? 'bg-eurobase-600' : 'bg-gray-200'}"
							>
								<span class="pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out {requireEmailConfirmation ? 'translate-x-5' : 'translate-x-0'}"></span>
							</button>
						</div>

						<!-- Minimum password length -->
						<div>
							<label for="pwd-min" class="block text-sm font-medium text-gray-700">Minimum password length</label>
							<input
								id="pwd-min"
								type="number"
								min="8"
								max="128"
								bind:value={passwordMinLength}
								class="mt-1.5 block w-24 rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
							/>
						</div>

						<!-- Session duration -->
						<div>
							<label for="session-dur" class="block text-sm font-medium text-gray-700">Session duration</label>
							<select
								id="session-dur"
								bind:value={sessionDuration}
								class="mt-1.5 block w-48 rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
							>
								{#each sessionOptions as opt}
									<option value={opt.value}>{opt.label}</option>
								{/each}
							</select>
						</div>

						<!-- Allowed redirect URLs -->
						<div>
							<label for="redirect-urls" class="block text-sm font-medium text-gray-700">Allowed redirect URLs</label>
							<p class="text-xs text-gray-400 mt-0.5">One URL per line</p>
							<textarea
								id="redirect-urls"
								bind:value={redirectUrls}
								rows="2"
								class="mt-1.5 block w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm font-mono focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
							></textarea>
						</div>
					</div>
				</div>
			</div>

			<!-- Actions -->
			<div class="mt-8 space-y-3">
				<button
					onclick={handleSaveAuthConfig}
					disabled={savingAuth}
					class="inline-flex w-full items-center justify-center gap-2 rounded-lg bg-eurobase-600 px-5 py-3 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 transition-colors disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
				>
					{#if savingAuth}
						<svg class="h-4 w-4 animate-spin" fill="none" viewBox="0 0 24 24">
							<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
							<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
						</svg>
						Saving...
					{:else}
						Continue
					{/if}
				</button>
				<button
					onclick={handleSkipAuth}
					class="w-full text-center text-sm text-gray-500 hover:text-gray-700 transition-colors cursor-pointer py-1"
				>
					Use defaults and continue &rarr;
				</button>
			</div>
		</div>

	{:else}
		<!-- STEP 3: SUCCESS / GET STARTED -->
		<div>
			<div class="text-center">
				<div class="mx-auto flex h-14 w-14 items-center justify-center rounded-2xl bg-emerald-50">
					<svg class="h-7 w-7 text-emerald-600" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M9 12.75 11.25 15 15 9.75M21 12c0 1.268-.63 2.39-1.593 3.068a3.745 3.745 0 0 1-1.043 3.296 3.745 3.745 0 0 1-3.296 1.043A3.745 3.745 0 0 1 12 21c-1.268 0-2.39-.63-3.068-1.593a3.746 3.746 0 0 1-3.296-1.043 3.745 3.745 0 0 1-1.043-3.296A3.745 3.745 0 0 1 3 12c0-1.268.63-2.39 1.593-3.068a3.745 3.745 0 0 1 1.043-3.296 3.746 3.746 0 0 1 3.296-1.043A3.746 3.746 0 0 1 12 3c1.268 0 2.39.63 3.068 1.593a3.746 3.746 0 0 1 3.296 1.043 3.745 3.745 0 0 1 1.043 3.296A3.745 3.745 0 0 1 21 12Z" />
					</svg>
				</div>
				<h1 class="mt-4 text-2xl font-bold text-gray-900">{createdProject?.name} is ready!</h1>
				<p class="mt-2 text-sm text-gray-500 leading-relaxed">
					Your database has a sample <code class="rounded bg-gray-100 px-1 py-0.5 text-xs font-mono">todos</code> table with 3 rows. Try the quickstart below.
				</p>
			</div>

			{#if isResumedPro}
				<!-- Resumed Pro path (#410 review 🟡). The webhook
				     created this project server-side and discarded
				     the plaintext keys, so we can't display them
				     here honestly. Point the user at the API tab
				     where they can regenerate a fresh pair
				     deliberately. Avoids the "here are your keys:
				     <blank>" footgun. -->
				<div class="mt-6 rounded-lg border border-eurobase-200 bg-eurobase-50 px-4 py-3 flex items-start gap-2.5">
					<svg class="h-5 w-5 shrink-0 text-eurobase-600 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M2.25 18 9 11.25l4.306 4.307a11.95 11.95 0 0 1 5.814-5.518l2.74-1.22m0 0-5.94-2.281m5.94 2.28-2.28 5.941" />
					</svg>
					<div>
						<p class="text-sm font-medium text-eurobase-800">Generate your API keys when you're ready</p>
						<p class="text-xs text-eurobase-700 mt-0.5">
							Because your project was created by a webhook after payment, no keys were returned to the browser. Head to
							<a href="/p/{projectId}/settings" class="underline hover:no-underline">Project Settings → API Keys</a>
							and click Regenerate to mint a fresh pair — copy them somewhere safe as soon as they appear (the secret is only shown once).
						</p>
					</div>
				</div>
			{:else}
			<!-- Keys warning -->
			<div class="mt-6 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 flex items-start gap-2.5 animate-[pulse-warn_3s_ease-in-out_infinite]">
			<style>
				@keyframes pulse-warn {
					0%, 100% { background-color: rgb(255 251 235); border-color: rgb(253 230 138); }
					50% { background-color: rgb(254 215 170); border-color: rgb(251 191 36); }
				}
			</style>
				<svg class="h-5 w-5 shrink-0 text-amber-500 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z" />
				</svg>
				<div>
					<p class="text-sm font-medium text-amber-800">Save your keys now — they won't be shown again</p>
					<p class="text-xs text-amber-700 mt-0.5">Copy them to a safe place or download the .env file below. You can regenerate keys later in project Settings, but the current ones will be invalidated.</p>
				</div>
			</div>

			<!-- API Keys -->
			<div class="mt-4 grid grid-cols-1 gap-3 sm:grid-cols-2">
				<div class="rounded-xl border border-gray-200 bg-white p-4 shadow-sm">
					<div class="flex items-center justify-between">
						<p class="text-sm font-medium text-gray-700">Public Key</p>
						<button
							onclick={() => copyToClipboard(publicKey, 'public')}
							class="text-xs text-gray-500 hover:text-gray-700 transition-colors cursor-pointer"
						>
							{copiedField === 'public' ? 'Copied!' : 'Copy'}
						</button>
					</div>
					<code class="mt-1.5 block truncate rounded-lg bg-gray-50 border border-gray-100 px-3 py-2 text-xs font-mono text-gray-900">{publicKey}</code>
					<p class="mt-1 text-xs text-gray-400">Safe for client-side code</p>
				</div>
				<div class="rounded-xl border border-gray-200 bg-white p-4 shadow-sm">
					<div class="flex items-center justify-between">
						<div class="flex items-center gap-2">
							<p class="text-sm font-medium text-gray-700">Secret Key</p>
							<button
								onclick={() => showSecretKey = !showSecretKey}
								class="text-xs text-gray-400 hover:text-gray-600 transition-colors cursor-pointer"
							>
								{showSecretKey ? 'Hide' : 'Show'}
							</button>
						</div>
						<button
							onclick={() => copyToClipboard(secretKey, 'secret')}
							class="text-xs text-gray-500 hover:text-gray-700 transition-colors cursor-pointer"
						>
							{copiedField === 'secret' ? 'Copied!' : 'Copy'}
						</button>
					</div>
					<code class="mt-1.5 block truncate rounded-lg bg-gray-50 border border-gray-100 px-3 py-2 text-xs font-mono text-gray-900">
						{showSecretKey ? secretKey : '*'.repeat(38)}
					</code>
					<p class="mt-1 text-xs text-red-500">Never expose in client-side code</p>
				</div>
			</div>
			{/if}

			<!-- IDE Setup -->
			<a
				href="/p/{projectId}/connect"
				class="mt-4 flex items-center gap-4 rounded-xl border border-eurobase-200 bg-eurobase-50/50 p-4 hover:bg-eurobase-50 transition-colors group"
			>
				<div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-eurobase-600 text-white">
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M17.25 6.75 22.5 12l-5.25 5.25m-10.5 0L1.5 12l5.25-5.25m7.5-3-4.5 16.5" />
					</svg>
				</div>
				<div class="flex-1">
					<p class="text-sm font-semibold text-gray-900 group-hover:text-eurobase-700 transition-colors">Set up your IDE</p>
					<p class="text-xs text-gray-500">Download pre-configured files for Claude Code, Cursor, Windsurf, or any AI coding tool</p>
				</div>
				<svg class="h-5 w-5 text-gray-400 group-hover:text-eurobase-500 transition-colors shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" d="m8.25 4.5 7.5 7.5-7.5 7.5" />
				</svg>
			</a>

			{#if !isResumedPro}
			<!-- Tabs — gated on !isResumedPro because every snippet
			     (Quick Start / curl / .env) embeds publicKey /
			     secretKey which are blank for the resumed-Pro
			     path. See isResumedPro doc comment. Resumed users
			     get pointed at the API tab in the panel above and
			     the Connect tab via the IDE Setup link, which is
			     enough — they'll find the copy-paste snippets
			     there once they've generated a key pair. -->
			<div class="mt-6 border-b border-gray-200">
				<nav class="flex gap-6" aria-label="Tabs">
					<button
						onclick={() => activeTab = 'quickstart'}
						class="border-b-2 pb-3 text-sm font-medium transition-colors cursor-pointer {activeTab === 'quickstart' ? 'border-eurobase-600 text-eurobase-700' : 'border-transparent text-gray-500 hover:text-gray-700 hover:border-gray-300'}"
					>
						Quick Start
					</button>
					<button
						onclick={() => activeTab = 'curl'}
						class="border-b-2 pb-3 text-sm font-medium transition-colors cursor-pointer {activeTab === 'curl' ? 'border-eurobase-600 text-eurobase-700' : 'border-transparent text-gray-500 hover:text-gray-700 hover:border-gray-300'}"
					>
						cURL
					</button>
					<button
						onclick={() => activeTab = 'ide'}
						class="border-b-2 pb-3 text-sm font-medium transition-colors cursor-pointer {activeTab === 'ide' ? 'border-eurobase-600 text-eurobase-700' : 'border-transparent text-gray-500 hover:text-gray-700 hover:border-gray-300'}"
					>
						.env
					</button>
				</nav>
			</div>

			<!-- Tab content -->
			<div class="mt-4">
				{#if activeTab === 'quickstart'}
					<div class="relative rounded-xl border border-gray-200 bg-gray-900 overflow-hidden">
						<div class="flex items-center justify-between border-b border-gray-700 px-4 py-2.5">
							<span class="text-xs text-gray-400 font-mono">index.ts</span>
							<button
								onclick={() => copyToClipboard(`npm install ${sdkPkg}\n\n${quickStartCode}`, 'quickstart')}
								class="inline-flex items-center gap-1.5 rounded-md bg-gray-800 px-2.5 py-1 text-xs text-gray-300 hover:bg-gray-700 transition-colors cursor-pointer"
							>
								{copiedField === 'quickstart' ? 'Copied!' : 'Copy'}
							</button>
						</div>
						<pre class="p-4 text-sm text-gray-100 font-mono overflow-x-auto leading-relaxed"><code>npm install {sdkPkg}

{quickStartCode}</code></pre>
					</div>

				{:else if activeTab === 'curl'}
					<div class="relative rounded-xl border border-gray-200 bg-gray-900 overflow-hidden">
						<div class="flex items-center justify-between border-b border-gray-700 px-4 py-2.5">
							<span class="text-xs text-gray-400 font-mono">terminal</span>
							<button
								onclick={() => copyToClipboard(curlCommand, 'curl')}
								class="inline-flex items-center gap-1.5 rounded-md bg-gray-800 px-2.5 py-1 text-xs text-gray-300 hover:bg-gray-700 transition-colors cursor-pointer"
							>
								{copiedField === 'curl' ? 'Copied!' : 'Copy'}
							</button>
						</div>
						<pre class="p-4 text-sm text-gray-100 font-mono overflow-x-auto leading-relaxed"><code>{curlCommand}</code></pre>
					</div>

				{:else if activeTab === 'ide'}
					<div class="relative rounded-xl border border-gray-200 bg-gray-900 overflow-hidden">
						<div class="flex items-center justify-between border-b border-gray-700 px-4 py-2.5">
							<span class="text-xs text-gray-400 font-mono">.env</span>
							<button
								onclick={() => copyToClipboard(envTemplate, 'env')}
								class="inline-flex items-center gap-1.5 rounded-md bg-gray-800 px-2.5 py-1 text-xs text-gray-300 hover:bg-gray-700 transition-colors cursor-pointer"
							>
								{copiedField === 'env' ? 'Copied!' : 'Copy'}
							</button>
						</div>
						<pre class="p-4 text-sm text-gray-100 font-mono overflow-x-auto leading-relaxed"><code>{envTemplate}</code></pre>
					</div>
				{/if}
			</div>
			{/if}

			<!-- Actions -->
			<div class="mt-6">
				<button
					onclick={goToDashboard}
					class="inline-flex w-full items-center justify-center gap-2 rounded-lg bg-eurobase-600 px-5 py-3 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 transition-colors cursor-pointer"
				>
					Go to Dashboard
					<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M13.5 4.5 21 12m0 0-7.5 7.5M21 12H3" />
					</svg>
				</button>
			</div>

			<p class="mt-4 text-center text-xs text-gray-400">
				Need to change auth settings?
				<a href="/p/{projectId}/auth" class="text-eurobase-600 hover:text-eurobase-500">Configure in Auth settings</a>
			</p>
		</div>
	{/if}
</div>
