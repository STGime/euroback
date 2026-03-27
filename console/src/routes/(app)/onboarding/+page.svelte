<script lang="ts">
	import { goto } from '$app/navigation';
	import { onMount } from 'svelte';
	import { api, type Project, type AuthConfig, type PlanLimits } from '$lib/api.js';
	import { loadProjects } from '$lib/stores.js';

	// State
	let projectName = $state('');
	let plan = $state('free');
	let creating = $state(false);
	let createError = $state('');
	let planData = $state<PlanLimits[]>([]);

	onMount(async () => {
		try {
			planData = await api.getPlans();
		} catch {
			// Use empty — cards will show hardcoded fallback
		}
	});

	let freePlan = $derived(planData.find(p => p.plan === 'free'));
	let proPlan = $derived(planData.find(p => p.plan === 'pro'));

	function formatLimit(mb: number): string {
		if (mb >= 1024) return (mb / 1024).toFixed(0) + ' GB';
		return mb + ' MB';
	}
	let createdProject = $state<Project | null>(null);
	let step = $state<'create' | 'auth' | 'success'>('create');

	// Auth config state (Step 2)
	let emailPasswordEnabled = $state(true);
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

	async function handleCreate() {
		if (!projectName.trim()) return;
		creating = true;
		createError = '';
		try {
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
			const msg = err instanceof Error ? err.message : 'Failed to create project';
			if (msg.includes('409') || msg.includes('already taken')) {
				const suffix = Math.random().toString(36).slice(2, 6);
				projectName = projectName.trim() + '-' + suffix;
				createError = `That project URL was taken. We've updated the name — click Create Project to try again, or edit it.`;
			} else if (msg.includes('limited to') && msg.includes('project')) {
				const limit = freePlan?.project_limit ?? 2;
				createError = `You've reached the maximum of ${limit} projects on the Free plan. Upgrade to Pro to create up to ${proPlan?.project_limit ?? 10} projects.`;
			} else {
				// Strip raw API prefix for cleaner display
				createError = msg.replace(/^API \d+:\s*/, '').replace(/^\{.*"error"\s*:\s*"/, '').replace(/"\s*\}$/, '');
			}
		} finally {
			creating = false;
		}
	}

	async function handleSaveAuthConfig() {
		if (!createdProject) return;
		savingAuth = true;
		authError = '';
		try {
			const config: AuthConfig = {
				providers: { email_password: { enabled: emailPasswordEnabled } },
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
					<legend class="block text-sm font-medium text-gray-700">Plan</legend>
					<div class="mt-2 grid grid-cols-1 gap-3 sm:grid-cols-2">
						<label class="cursor-pointer">
							<input type="radio" name="onb-plan" value="free" bind:group={plan} class="peer sr-only" />
							<div class="rounded-xl border-2 p-4 transition-all peer-checked:border-eurobase-600 peer-checked:bg-eurobase-50/50 peer-checked:shadow-sm border-gray-200 hover:border-gray-300">
								<div class="flex items-center justify-between">
									<p class="text-sm font-semibold text-gray-900">Free</p>
									<span class="text-xs font-medium text-gray-400">$0/mo</span>
								</div>
								<ul class="mt-2.5 space-y-1 text-xs text-gray-500">
									<li class="flex items-center gap-1.5">
										<svg class="h-3.5 w-3.5 text-gray-400" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
										{freePlan ? formatLimit(freePlan.db_size_mb) : '500 MB'} database
									</li>
									<li class="flex items-center gap-1.5">
										<svg class="h-3.5 w-3.5 text-gray-400" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
										{freePlan ? formatLimit(freePlan.storage_mb) : '1 GB'} file storage
									</li>
									<li class="flex items-center gap-1.5">
										<svg class="h-3.5 w-3.5 text-gray-400" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
										{freePlan ? (freePlan.mau_limit / 1000).toFixed(0) + 'k' : '10k'} auth users
									</li>
								</ul>
							</div>
						</label>
						<label class="cursor-pointer">
							<input type="radio" name="onb-plan" value="pro" bind:group={plan} class="peer sr-only" />
							<div class="rounded-xl border-2 p-4 transition-all peer-checked:border-eurobase-600 peer-checked:bg-eurobase-50/50 peer-checked:shadow-sm border-gray-200 hover:border-gray-300">
								<div class="flex items-center justify-between">
									<p class="text-sm font-semibold text-gray-900">Pro</p>
									<span class="text-sm font-semibold text-eurobase-700">&euro;19/mo</span>
								</div>
								<ul class="mt-2.5 space-y-1 text-xs text-gray-500">
									<li class="flex items-center gap-1.5">
										<svg class="h-3.5 w-3.5 text-eurobase-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
										{proPlan ? formatLimit(proPlan.db_size_mb) : '5 GB'} database
									</li>
									<li class="flex items-center gap-1.5">
										<svg class="h-3.5 w-3.5 text-eurobase-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
										{proPlan ? formatLimit(proPlan.storage_mb) : '50 GB'} file storage
									</li>
									<li class="flex items-center gap-1.5">
										<svg class="h-3.5 w-3.5 text-eurobase-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
										{proPlan ? (proPlan.mau_limit / 1000).toFixed(0) + 'k' : '100k'} auth users
									</li>
								</ul>
							</div>
						</label>
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

						<!-- Passkeys (coming soon) -->
						<div class="flex items-center justify-between rounded-lg border border-gray-200 px-4 py-3 opacity-50 cursor-not-allowed">
							<div>
								<p class="text-sm font-medium text-gray-900">Passkeys</p>
								<p class="text-xs text-gray-500">Passwordless auth with WebAuthn</p>
							</div>
							<span class="inline-flex items-center rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-500">Coming soon</span>
						</div>

						<!-- Social Login (coming soon) -->
						<div class="flex items-center justify-between rounded-lg border border-gray-200 px-4 py-3 opacity-50 cursor-not-allowed">
							<div>
								<p class="text-sm font-medium text-gray-900">Social Login (Google, GitHub)</p>
								<p class="text-xs text-gray-500">Let users sign in with existing accounts</p>
							</div>
							<span class="inline-flex items-center rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-500">Coming soon</span>
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

			<!-- Tabs -->
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
