<script lang="ts">
	import { goto } from '$app/navigation';
	import { api, type Project } from '$lib/api.js';
	import { loadProjects } from '$lib/stores.js';

	// Step state
	let step = $state(1);
	let transitioning = $state(false);

	// Step 1: Project info
	let projectName = $state('');
	let plan = $state('free');
	let creating = $state(false);
	let createError = $state('');
	let createdProject = $state<Project | null>(null);

	// Step 2: Auth config
	let passkeysEnabled = $state(true);
	let emailPasswordEnabled = $state(true);
	let socialLoginEnabled = $state(false);
	let redirectUrls = $state('http://localhost:3000');

	// Step 3: Tab state
	let activeTab = $state<'quickstart' | 'apikeys' | 'nextsteps'>('quickstart');
	let showSecretKey = $state(false);
	let copiedField = $state('');

	// Derived slug
	let slug = $derived(
		projectName
			.toLowerCase()
			.replace(/[^a-z0-9\s-]/g, '')
			.replace(/\s+/g, '-')
			.replace(/-+/g, '-')
			.replace(/^-|-$/g, '')
	);

	// Generated keys (simulated)
	let publicKey = $derived(
		createdProject ? `eb_pk_${createdProject.id.replace(/-/g, '').slice(0, 24)}` : 'eb_pk_...'
	);
	let secretKey = $derived(
		createdProject ? `eb_sk_${createdProject.id.replace(/-/g, '').slice(0, 24)}` : 'eb_sk_...'
	);
	let projectSlug = $derived(createdProject?.slug ?? slug);
	let projectId = $derived(createdProject?.id ?? '');

	function goToStep(target: number) {
		transitioning = true;
		setTimeout(() => {
			step = target;
			transitioning = false;
		}, 150);
	}

	async function handleStep1Continue() {
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
			goToStep(2);
		} catch (err) {
			createError = err instanceof Error ? err.message : 'Failed to create project';
		} finally {
			creating = false;
		}
	}

	function handleStep2Continue() {
		goToStep(3);
	}

	function handleStep2Skip() {
		goToStep(3);
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
			setTimeout(() => {
				copiedField = '';
			}, 2000);
		} catch {
			// Fallback — silently fail
		}
	}

	const sdkPkg = '@eurobase/sdk';
	let quickStartCode = $derived(`npm install ${sdkPkg}

import { createClient } from '${sdkPkg}'
const eb = createClient({
  url: 'https://${projectSlug}.eurobase.app',
  apiKey: '${publicKey}'
})

const { data } = await eb.db.from('todos').select('*')`);
</script>

<svelte:head>
	<title>Create your first project - Eurobase Console</title>
</svelte:head>

<div class="mx-auto max-w-3xl">
	<!-- Step indicator -->
	<div class="mb-10">
		<div class="flex items-center justify-center">
			{#each [1, 2, 3] as s}
				{#if s > 1}
					<div class="mx-1 h-0.5 w-12 sm:w-20 rounded-full transition-colors duration-300 {s <= step ? 'bg-eurobase-600' : 'bg-gray-200'}"></div>
				{/if}
				<div class="relative flex flex-col items-center">
					<div
						class="flex h-9 w-9 items-center justify-center rounded-full text-sm font-semibold transition-all duration-300
						{s < step ? 'bg-eurobase-600 text-white' : s === step ? 'bg-eurobase-600 text-white ring-4 ring-eurobase-100' : 'bg-gray-200 text-gray-500'}"
					>
						{#if s < step}
							<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2.5" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" />
							</svg>
						{:else}
							{s}
						{/if}
					</div>
					<span class="absolute -bottom-6 whitespace-nowrap text-xs font-medium {s <= step ? 'text-eurobase-700' : 'text-gray-400'}">
						{#if s === 1}Project{:else if s === 2}Auth{:else}Connect{/if}
					</span>
				</div>
			{/each}
		</div>
	</div>

	<!-- Step content with fade transition -->
	<div class="mt-10 transition-opacity duration-150 {transitioning ? 'opacity-0' : 'opacity-100'}">

		<!-- STEP 1: Name your project -->
		{#if step === 1}
			<div>
				<h1 class="text-2xl font-bold text-gray-900">Name your project</h1>
				<p class="mt-2 text-sm text-gray-500 leading-relaxed">
					A project is your backend. It contains a database, file storage, and authentication — everything your app needs.
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
							<!-- Free plan card -->
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
											500 MB database
										</li>
										<li class="flex items-center gap-1.5">
											<svg class="h-3.5 w-3.5 text-gray-400" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
											1 GB file storage
										</li>
										<li class="flex items-center gap-1.5">
											<svg class="h-3.5 w-3.5 text-gray-400" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
											50k API requests/mo
										</li>
									</ul>
								</div>
							</label>
							<!-- Pro plan card -->
							<label class="cursor-pointer">
								<input type="radio" name="onb-plan" value="pro" bind:group={plan} class="peer sr-only" />
								<div class="rounded-xl border-2 p-4 transition-all peer-checked:border-eurobase-600 peer-checked:bg-eurobase-50/50 peer-checked:shadow-sm border-gray-200 hover:border-gray-300">
									<div class="flex items-center justify-between">
										<p class="text-sm font-semibold text-gray-900">Pro</p>
										<span class="text-sm font-semibold text-eurobase-700">&euro;29/mo</span>
									</div>
									<ul class="mt-2.5 space-y-1 text-xs text-gray-500">
										<li class="flex items-center gap-1.5">
											<svg class="h-3.5 w-3.5 text-eurobase-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
											8 GB database
										</li>
										<li class="flex items-center gap-1.5">
											<svg class="h-3.5 w-3.5 text-eurobase-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
											100 GB file storage
										</li>
										<li class="flex items-center gap-1.5">
											<svg class="h-3.5 w-3.5 text-eurobase-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
											Unlimited API requests
										</li>
									</ul>
								</div>
							</label>
						</div>
					</fieldset>
				</div>

				<!-- Continue button -->
				<div class="mt-8">
					<button
						onclick={handleStep1Continue}
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
							Continue
							<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" d="M13.5 4.5 21 12m0 0-7.5 7.5M21 12H3" />
							</svg>
						{/if}
					</button>
				</div>
			</div>

		<!-- STEP 2: Set up authentication -->
		{:else if step === 2}
			<div>
				<h1 class="text-2xl font-bold text-gray-900">Set up authentication</h1>
				<p class="mt-2 text-sm text-gray-500 leading-relaxed">
					Eurobase uses Hanko for authentication — a privacy-first, EU-based auth provider.
				</p>

				<div class="mt-6 grid grid-cols-1 gap-6 lg:grid-cols-5">
					<!-- Auth toggles (left column) -->
					<div class="lg:col-span-3 space-y-4">
						<!-- Passkeys toggle -->
						<div class="flex items-center justify-between rounded-xl border border-gray-200 bg-white p-4 shadow-sm">
							<div class="flex items-center gap-3">
								<div class="flex h-10 w-10 items-center justify-center rounded-lg bg-eurobase-50 text-eurobase-600">
									<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
										<path stroke-linecap="round" stroke-linejoin="round" d="M7.864 4.243A7.5 7.5 0 0 1 19.5 10.5c0 2.92-.556 5.709-1.568 8.268M5.742 6.364A7.465 7.465 0 0 0 4.5 10.5a7.464 7.464 0 0 1-1.15 3.993m1.989 3.559A11.209 11.209 0 0 0 8.25 10.5a3.75 3.75 0 1 1 7.5 0c0 .527-.021 1.049-.064 1.565M12 10.5a14.94 14.94 0 0 1-3.6 9.75m6.633-4.596a18.666 18.666 0 0 1-2.485 5.33" />
									</svg>
								</div>
								<div>
									<p class="text-sm font-semibold text-gray-900">Passkeys</p>
									<p class="text-xs text-gray-500">Passwordless biometric login</p>
								</div>
							</div>
							<div class="flex items-center gap-2">
								<span class="rounded-full bg-eurobase-50 px-2 py-0.5 text-xs font-medium text-eurobase-700">Recommended</span>
								<button
									type="button"
									onclick={() => passkeysEnabled = !passkeysEnabled}
									aria-label="Toggle passkeys"
									class="relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 {passkeysEnabled ? 'bg-eurobase-600' : 'bg-gray-200'}"
									role="switch"
									aria-checked={passkeysEnabled}
								>
									<span class="pointer-events-none inline-block h-5 w-5 rounded-full bg-white shadow ring-0 transition-transform duration-200 {passkeysEnabled ? 'translate-x-5' : 'translate-x-0'}"></span>
								</button>
							</div>
						</div>

						<!-- Email + Password toggle -->
						<div class="flex items-center justify-between rounded-xl border border-gray-200 bg-white p-4 shadow-sm">
							<div class="flex items-center gap-3">
								<div class="flex h-10 w-10 items-center justify-center rounded-lg bg-blue-50 text-blue-600">
									<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
										<path stroke-linecap="round" stroke-linejoin="round" d="M21.75 6.75v10.5a2.25 2.25 0 0 1-2.25 2.25h-15a2.25 2.25 0 0 1-2.25-2.25V6.75m19.5 0A2.25 2.25 0 0 0 19.5 4.5h-15a2.25 2.25 0 0 0-2.25 2.25m19.5 0v.243a2.25 2.25 0 0 1-1.07 1.916l-7.5 4.615a2.25 2.25 0 0 1-2.36 0L3.32 8.91a2.25 2.25 0 0 1-1.07-1.916V6.75" />
									</svg>
								</div>
								<div>
									<p class="text-sm font-semibold text-gray-900">Email + Password</p>
									<p class="text-xs text-gray-500">Traditional email and password login</p>
								</div>
							</div>
							<button
								type="button"
								onclick={() => emailPasswordEnabled = !emailPasswordEnabled}
								aria-label="Toggle email and password"
								class="relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 {emailPasswordEnabled ? 'bg-eurobase-600' : 'bg-gray-200'}"
								role="switch"
								aria-checked={emailPasswordEnabled}
							>
								<span class="pointer-events-none inline-block h-5 w-5 rounded-full bg-white shadow ring-0 transition-transform duration-200 {emailPasswordEnabled ? 'translate-x-5' : 'translate-x-0'}"></span>
							</button>
						</div>

						<!-- Social Login toggle (disabled) -->
						<div class="flex items-center justify-between rounded-xl border border-gray-200 bg-white p-4 shadow-sm opacity-60">
							<div class="flex items-center gap-3">
								<div class="flex h-10 w-10 items-center justify-center rounded-lg bg-gray-100 text-gray-400">
									<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
										<path stroke-linecap="round" stroke-linejoin="round" d="M12 21a9.004 9.004 0 0 0 8.716-6.747M12 21a9.004 9.004 0 0 1-8.716-6.747M12 21c2.485 0 4.5-4.03 4.5-9S14.485 3 12 3m0 18c-2.485 0-4.5-4.03-4.5-9S9.515 3 12 3m0 0a8.997 8.997 0 0 1 7.843 4.582M12 3a8.997 8.997 0 0 0-7.843 4.582m15.686 0A11.953 11.953 0 0 1 12 10.5c-2.998 0-5.74-1.1-7.843-2.918m15.686 0A8.959 8.959 0 0 1 21 12c0 .778-.099 1.533-.284 2.253m0 0A17.919 17.919 0 0 1 12 16.5c-3.162 0-6.133-.815-8.716-2.247m0 0A9.015 9.015 0 0 1 3 12c0-1.605.42-3.113 1.157-4.418" />
									</svg>
								</div>
								<div>
									<p class="text-sm font-semibold text-gray-900">Social Login</p>
									<p class="text-xs text-gray-500">Google, GitHub, etc.</p>
								</div>
							</div>
							<div class="flex items-center gap-2">
								<span class="rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-500">Coming soon</span>
								<button
									type="button"
									disabled
									aria-label="Toggle social login"
									class="relative inline-flex h-6 w-11 shrink-0 cursor-not-allowed rounded-full border-2 border-transparent bg-gray-200 transition-colors duration-200"
									role="switch"
									aria-checked="false"
								>
									<span class="pointer-events-none inline-block h-5 w-5 translate-x-0 rounded-full bg-white shadow ring-0"></span>
								</button>
							</div>
						</div>

						<!-- Redirect URLs -->
						<div class="mt-2">
							<label for="onb-redirect" class="block text-sm font-medium text-gray-700">Redirect URLs</label>
							<input
								id="onb-redirect"
								type="text"
								bind:value={redirectUrls}
								placeholder="http://localhost:3000"
								class="mt-1.5 block w-full rounded-lg border border-gray-300 px-3.5 py-2.5 text-sm text-gray-900 shadow-sm placeholder:text-gray-400 focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors font-mono"
							/>
							<p class="mt-1 text-xs text-gray-400">Comma-separated list of allowed redirect URLs</p>
						</div>
					</div>

					<!-- Login form preview (right column) -->
					<div class="lg:col-span-2">
						<div class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
							<p class="mb-4 text-xs font-medium text-gray-400 uppercase tracking-wider">Login preview</p>
							<div class="space-y-3">
								<div class="text-center">
									<div class="mx-auto flex h-10 w-10 items-center justify-center rounded-lg bg-eurobase-600">
										<svg class="h-5 w-5 text-white" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
											<path stroke-linecap="round" stroke-linejoin="round" d="M9 12.75 11.25 15 15 9.75m-3-7.036A11.959 11.959 0 0 1 3.598 6 11.99 11.99 0 0 0 3 9.749c0 5.592 3.824 10.29 9 11.623 5.176-1.332 9-6.03 9-11.622 0-1.31-.21-2.571-.598-3.751h-.152c-3.196 0-6.1-1.248-8.25-3.285Z" />
										</svg>
									</div>
									<p class="mt-2 text-sm font-semibold text-gray-900">Sign in</p>
								</div>
								{#if emailPasswordEnabled}
									<div>
										<div class="h-8 rounded-md border border-gray-200 bg-gray-50 px-2 flex items-center">
											<span class="text-xs text-gray-400">email@example.com</span>
										</div>
									</div>
									<div>
										<div class="h-8 rounded-md border border-gray-200 bg-gray-50 px-2 flex items-center">
											<span class="text-xs text-gray-400">Password</span>
										</div>
									</div>
									<div class="h-8 rounded-md bg-eurobase-600 flex items-center justify-center">
										<span class="text-xs font-medium text-white">Sign in</span>
									</div>
								{/if}
								{#if passkeysEnabled && emailPasswordEnabled}
									<div class="flex items-center gap-2">
										<div class="flex-1 h-px bg-gray-200"></div>
										<span class="text-xs text-gray-400">or</span>
										<div class="flex-1 h-px bg-gray-200"></div>
									</div>
								{/if}
								{#if passkeysEnabled}
									<div class="h-8 rounded-md border border-gray-200 bg-white flex items-center justify-center gap-1.5">
										<svg class="h-3.5 w-3.5 text-gray-500" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
											<path stroke-linecap="round" stroke-linejoin="round" d="M7.864 4.243A7.5 7.5 0 0 1 19.5 10.5c0 2.92-.556 5.709-1.568 8.268M5.742 6.364A7.465 7.465 0 0 0 4.5 10.5a7.464 7.464 0 0 1-1.15 3.993m1.989 3.559A11.209 11.209 0 0 0 8.25 10.5a3.75 3.75 0 1 1 7.5 0c0 .527-.021 1.049-.064 1.565M12 10.5a14.94 14.94 0 0 1-3.6 9.75m6.633-4.596a18.666 18.666 0 0 1-2.485 5.33" />
										</svg>
										<span class="text-xs font-medium text-gray-700">Sign in with passkey</span>
									</div>
								{/if}
								{#if !passkeysEnabled && !emailPasswordEnabled}
									<div class="py-4 text-center">
										<p class="text-xs text-gray-400">No auth methods enabled</p>
									</div>
								{/if}
							</div>
						</div>
					</div>
				</div>

				<!-- Continue / Skip buttons -->
				<div class="mt-8 flex flex-col items-center gap-3 sm:flex-row sm:justify-between">
					<button
						onclick={handleStep2Skip}
						class="text-sm text-gray-500 hover:text-gray-700 transition-colors cursor-pointer order-2 sm:order-1"
					>
						Use defaults and skip
					</button>
					<button
						onclick={handleStep2Continue}
						class="inline-flex w-full sm:w-auto items-center justify-center gap-2 rounded-lg bg-eurobase-600 px-6 py-3 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 transition-colors cursor-pointer order-1 sm:order-2"
					>
						Continue
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M13.5 4.5 21 12m0 0-7.5 7.5M21 12H3" />
						</svg>
					</button>
				</div>
			</div>

		<!-- STEP 3: Your backend is ready! -->
		{:else if step === 3}
			<div>
				<div class="text-center">
					<div class="mx-auto flex h-14 w-14 items-center justify-center rounded-2xl bg-emerald-50">
						<svg class="h-7 w-7 text-emerald-600" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M9 12.75 11.25 15 15 9.75M21 12c0 1.268-.63 2.39-1.593 3.068a3.745 3.745 0 0 1-1.043 3.296 3.745 3.745 0 0 1-3.296 1.043A3.745 3.745 0 0 1 12 21c-1.268 0-2.39-.63-3.068-1.593a3.746 3.746 0 0 1-3.296-1.043 3.745 3.745 0 0 1-1.043-3.296A3.745 3.745 0 0 1 3 12c0-1.268.63-2.39 1.593-3.068a3.745 3.745 0 0 1 1.043-3.296 3.746 3.746 0 0 1 3.296-1.043A3.746 3.746 0 0 1 12 3c1.268 0 2.39.63 3.068 1.593a3.746 3.746 0 0 1 3.296 1.043 3.745 3.745 0 0 1 1.043 3.296A3.745 3.745 0 0 1 21 12Z" />
						</svg>
					</div>
					<h1 class="mt-4 text-2xl font-bold text-gray-900">Your backend is ready!</h1>
					<p class="mt-2 text-sm text-gray-500 leading-relaxed">
						Your database, storage, and auth are live. Here's how to connect your app.
					</p>
				</div>

				<!-- Tabs -->
				<div class="mt-8 border-b border-gray-200">
					<nav class="flex gap-6" aria-label="Tabs">
						<button
							onclick={() => activeTab = 'quickstart'}
							class="border-b-2 pb-3 text-sm font-medium transition-colors cursor-pointer {activeTab === 'quickstart' ? 'border-eurobase-600 text-eurobase-700' : 'border-transparent text-gray-500 hover:text-gray-700 hover:border-gray-300'}"
						>
							Quick Start
						</button>
						<button
							onclick={() => activeTab = 'apikeys'}
							class="border-b-2 pb-3 text-sm font-medium transition-colors cursor-pointer {activeTab === 'apikeys' ? 'border-eurobase-600 text-eurobase-700' : 'border-transparent text-gray-500 hover:text-gray-700 hover:border-gray-300'}"
						>
							API Keys
						</button>
						<button
							onclick={() => activeTab = 'nextsteps'}
							class="border-b-2 pb-3 text-sm font-medium transition-colors cursor-pointer {activeTab === 'nextsteps' ? 'border-eurobase-600 text-eurobase-700' : 'border-transparent text-gray-500 hover:text-gray-700 hover:border-gray-300'}"
						>
							Next Steps
						</button>
					</nav>
				</div>

				<!-- Tab content -->
				<div class="mt-6">
					{#if activeTab === 'quickstart'}
						<div class="relative rounded-xl border border-gray-200 bg-gray-900 overflow-hidden">
							<div class="flex items-center justify-between border-b border-gray-700 px-4 py-2.5">
								<span class="text-xs text-gray-400 font-mono">terminal</span>
								<button
									onclick={() => copyToClipboard(quickStartCode, 'quickstart')}
									class="inline-flex items-center gap-1.5 rounded-md bg-gray-800 px-2.5 py-1 text-xs text-gray-300 hover:bg-gray-700 transition-colors cursor-pointer"
								>
									{#if copiedField === 'quickstart'}
										<svg class="h-3.5 w-3.5 text-emerald-400" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
											<path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" />
										</svg>
										Copied
									{:else}
										<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
											<path stroke-linecap="round" stroke-linejoin="round" d="M15.666 3.888A2.25 2.25 0 0 0 13.5 2.25h-3c-1.03 0-1.9.693-2.166 1.638m7.332 0c.055.194.084.4.084.612v0a.75.75 0 0 1-.75.75H9.75a.75.75 0 0 1-.75-.75v0c0-.212.03-.418.084-.612m7.332 0c.646.049 1.288.11 1.927.184 1.1.128 1.907 1.077 1.907 2.185V19.5a2.25 2.25 0 0 1-2.25 2.25H6.75A2.25 2.25 0 0 1 4.5 19.5V6.257c0-1.108.806-2.057 1.907-2.185a48.208 48.208 0 0 1 1.927-.184" />
										</svg>
										Copy
									{/if}
								</button>
							</div>
							<pre class="p-4 text-sm text-gray-100 font-mono overflow-x-auto leading-relaxed"><code>{quickStartCode}</code></pre>
						</div>

					{:else if activeTab === 'apikeys'}
						<div class="space-y-4">
							<!-- Public key -->
							<div class="rounded-xl border border-gray-200 bg-white p-4 shadow-sm">
								<div class="flex items-center justify-between">
									<div>
										<p class="text-sm font-medium text-gray-700">Public Key</p>
										<p class="mt-0.5 text-xs text-gray-400">Safe to use in client-side code</p>
									</div>
									<button
										onclick={() => copyToClipboard(publicKey, 'public')}
										class="inline-flex items-center gap-1.5 rounded-md border border-gray-200 px-2.5 py-1.5 text-xs text-gray-600 hover:bg-gray-50 transition-colors cursor-pointer"
									>
										{#if copiedField === 'public'}
											<svg class="h-3.5 w-3.5 text-emerald-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
												<path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" />
											</svg>
											Copied
										{:else}
											<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
												<path stroke-linecap="round" stroke-linejoin="round" d="M15.666 3.888A2.25 2.25 0 0 0 13.5 2.25h-3c-1.03 0-1.9.693-2.166 1.638m7.332 0c.055.194.084.4.084.612v0a.75.75 0 0 1-.75.75H9.75a.75.75 0 0 1-.75-.75v0c0-.212.03-.418.084-.612m7.332 0c.646.049 1.288.11 1.927.184 1.1.128 1.907 1.077 1.907 2.185V19.5a2.25 2.25 0 0 1-2.25 2.25H6.75A2.25 2.25 0 0 1 4.5 19.5V6.257c0-1.108.806-2.057 1.907-2.185a48.208 48.208 0 0 1 1.927-.184" />
											</svg>
											Copy
										{/if}
									</button>
								</div>
								<div class="mt-2 rounded-lg bg-gray-50 border border-gray-100 px-3.5 py-2.5">
									<code class="text-sm font-mono text-gray-900">{publicKey}</code>
								</div>
							</div>

							<!-- Secret key -->
							<div class="rounded-xl border border-gray-200 bg-white p-4 shadow-sm">
								<div class="flex items-center justify-between">
									<div>
										<p class="text-sm font-medium text-gray-700">Secret Key</p>
										<p class="mt-0.5 text-xs text-red-500">Never expose in client-side code</p>
									</div>
									<div class="flex items-center gap-2">
										<button
											onclick={() => showSecretKey = !showSecretKey}
											class="inline-flex items-center gap-1.5 rounded-md border border-gray-200 px-2.5 py-1.5 text-xs text-gray-600 hover:bg-gray-50 transition-colors cursor-pointer"
										>
											{#if showSecretKey}
												<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
													<path stroke-linecap="round" stroke-linejoin="round" d="M3.98 8.223A10.477 10.477 0 0 0 1.934 12C3.226 16.338 7.244 19.5 12 19.5c.993 0 1.953-.138 2.863-.395M6.228 6.228A10.451 10.451 0 0 1 12 4.5c4.756 0 8.773 3.162 10.065 7.498a10.522 10.522 0 0 1-4.293 5.774M6.228 6.228 3 3m3.228 3.228 3.65 3.65m7.894 7.894L21 21m-3.228-3.228-3.65-3.65m0 0a3 3 0 1 0-4.243-4.243m4.242 4.242L9.88 9.88" />
												</svg>
												Hide
											{:else}
												<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
													<path stroke-linecap="round" stroke-linejoin="round" d="M2.036 12.322a1.012 1.012 0 0 1 0-.639C3.423 7.51 7.36 4.5 12 4.5c4.638 0 8.573 3.007 9.963 7.178.07.207.07.431 0 .639C20.577 16.49 16.64 19.5 12 19.5c-4.638 0-8.573-3.007-9.963-7.178Z" />
													<path stroke-linecap="round" stroke-linejoin="round" d="M15 12a3 3 0 1 1-6 0 3 3 0 0 1 6 0Z" />
												</svg>
												Show
											{/if}
										</button>
										<button
											onclick={() => copyToClipboard(secretKey, 'secret')}
											class="inline-flex items-center gap-1.5 rounded-md border border-gray-200 px-2.5 py-1.5 text-xs text-gray-600 hover:bg-gray-50 transition-colors cursor-pointer"
										>
											{#if copiedField === 'secret'}
												<svg class="h-3.5 w-3.5 text-emerald-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
													<path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" />
												</svg>
												Copied
											{:else}
												<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
													<path stroke-linecap="round" stroke-linejoin="round" d="M15.666 3.888A2.25 2.25 0 0 0 13.5 2.25h-3c-1.03 0-1.9.693-2.166 1.638m7.332 0c.055.194.084.4.084.612v0a.75.75 0 0 1-.75.75H9.75a.75.75 0 0 1-.75-.75v0c0-.212.03-.418.084-.612m7.332 0c.646.049 1.288.11 1.927.184 1.1.128 1.907 1.077 1.907 2.185V19.5a2.25 2.25 0 0 1-2.25 2.25H6.75A2.25 2.25 0 0 1 4.5 19.5V6.257c0-1.108.806-2.057 1.907-2.185a48.208 48.208 0 0 1 1.927-.184" />
												</svg>
												Copy
											{/if}
										</button>
									</div>
								</div>
								<div class="mt-2 rounded-lg bg-gray-50 border border-gray-100 px-3.5 py-2.5">
									{#if showSecretKey}
										<code class="text-sm font-mono text-gray-900">{secretKey}</code>
									{:else}
										<code class="text-sm font-mono text-gray-400">{'*'.repeat(32)}</code>
									{/if}
								</div>
							</div>
						</div>

					{:else if activeTab === 'nextsteps'}
						<div class="space-y-3">
							<a
								href="/p/{projectId}/database"
								class="flex items-center gap-3 rounded-xl border border-gray-200 bg-white p-4 shadow-sm hover:border-eurobase-300 hover:shadow-md transition-all group"
							>
								<div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-eurobase-50 text-eurobase-600 group-hover:bg-eurobase-100 transition-colors">
									<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
										<path stroke-linecap="round" stroke-linejoin="round" d="M3.375 19.5h17.25m-17.25 0a1.125 1.125 0 0 1-1.125-1.125M3.375 19.5h7.5c.621 0 1.125-.504 1.125-1.125m-9.75 0V5.625m0 12.75v-1.5c0-.621.504-1.125 1.125-1.125m18.375 2.625V5.625m0 12.75c0 .621-.504 1.125-1.125 1.125m1.125-1.125v-1.5c0-.621-.504-1.125-1.125-1.125m0 3.75h-7.5A1.125 1.125 0 0 1 12 18.375m9.75-12.75c0-.621-.504-1.125-1.125-1.125H3.375c-.621 0-1.125.504-1.125 1.125m19.5 0v1.5c0 .621-.504 1.125-1.125 1.125M2.25 5.625v1.5c0 .621.504 1.125 1.125 1.125m0 0h17.25m-17.25 0h7.5c.621 0 1.125.504 1.125 1.125M3.375 8.25c-.621 0-1.125.504-1.125 1.125v1.5c0 .621.504 1.125 1.125 1.125m17.25-3.75h-7.5c-.621 0-1.125.504-1.125 1.125m8.625-1.125c.621 0 1.125.504 1.125 1.125v1.5c0 .621-.504 1.125-1.125 1.125m-17.25 0h7.5m-7.5 0c-.621 0-1.125.504-1.125 1.125v1.5c0 .621.504 1.125 1.125 1.125M12 10.875v-1.5m0 1.5c0 .621-.504 1.125-1.125 1.125M12 10.875c0 .621.504 1.125 1.125 1.125m-2.25 0c.621 0 1.125.504 1.125 1.125M10.875 12c-.621 0-1.125.504-1.125 1.125M12 12c.621 0 1.125.504 1.125 1.125m0 0v1.5c0 .621-.504 1.125-1.125 1.125M12 15.375c0-.621-.504-1.125-1.125-1.125" />
									</svg>
								</div>
								<div class="flex-1">
									<p class="text-sm font-semibold text-gray-900 group-hover:text-eurobase-700 transition-colors">Create your first table</p>
									<p class="text-xs text-gray-500">Define your database schema with the visual editor</p>
								</div>
								<svg class="h-4 w-4 text-gray-400 group-hover:text-eurobase-500 transition-colors" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
									<path stroke-linecap="round" stroke-linejoin="round" d="m8.25 4.5 7.5 7.5-7.5 7.5" />
								</svg>
							</a>

							<a
								href="/p/{projectId}/storage"
								class="flex items-center gap-3 rounded-xl border border-gray-200 bg-white p-4 shadow-sm hover:border-eurobase-300 hover:shadow-md transition-all group"
							>
								<div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-amber-50 text-amber-600 group-hover:bg-amber-100 transition-colors">
									<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
										<path stroke-linecap="round" stroke-linejoin="round" d="M3 16.5v2.25A2.25 2.25 0 0 0 5.25 21h13.5A2.25 2.25 0 0 0 21 18.75V16.5m-13.5-9L12 3m0 0 4.5 4.5M12 3v13.5" />
									</svg>
								</div>
								<div class="flex-1">
									<p class="text-sm font-semibold text-gray-900 group-hover:text-eurobase-700 transition-colors">Upload a file</p>
									<p class="text-xs text-gray-500">Store files in EU-sovereign object storage</p>
								</div>
								<svg class="h-4 w-4 text-gray-400 group-hover:text-eurobase-500 transition-colors" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
									<path stroke-linecap="round" stroke-linejoin="round" d="m8.25 4.5 7.5 7.5-7.5 7.5" />
								</svg>
							</a>

							<a
								href="/docs"
								class="flex items-center gap-3 rounded-xl border border-gray-200 bg-white p-4 shadow-sm hover:border-eurobase-300 hover:shadow-md transition-all group"
							>
								<div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-purple-50 text-purple-600 group-hover:bg-purple-100 transition-colors">
									<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
										<path stroke-linecap="round" stroke-linejoin="round" d="M12 6.042A8.967 8.967 0 0 0 6 3.75c-1.052 0-2.062.18-3 .512v14.25A8.987 8.987 0 0 1 6 18c2.305 0 4.408.867 6 2.292m0-14.25a8.966 8.966 0 0 1 6-2.292c1.052 0 2.062.18 3 .512v14.25A8.987 8.987 0 0 0 18 18a8.967 8.967 0 0 0-6 2.292m0-14.25v14.25" />
									</svg>
								</div>
								<div class="flex-1">
									<p class="text-sm font-semibold text-gray-900 group-hover:text-eurobase-700 transition-colors">Read the docs</p>
									<p class="text-xs text-gray-500">API reference, guides, and examples</p>
								</div>
								<svg class="h-4 w-4 text-gray-400 group-hover:text-eurobase-500 transition-colors" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
									<path stroke-linecap="round" stroke-linejoin="round" d="m8.25 4.5 7.5 7.5-7.5 7.5" />
								</svg>
							</a>
						</div>
					{/if}
				</div>

				<!-- Go to Dashboard button -->
				<div class="mt-8">
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
			</div>
		{/if}
	</div>
</div>
