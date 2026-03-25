<script lang="ts">
	import { getContext } from 'svelte';
	import { api, type AuthConfig } from '$lib/api.js';

	const projectCtx: { id: string; project: import('$lib/api.js').Project | null } = getContext('projectId');

	// Defaults
	const defaults: AuthConfig = {
		providers: { email_password: { enabled: true } },
		password_min_length: 8,
		require_email_confirmation: false,
		session_duration: '168h',
		redirect_urls: ['http://localhost:3000']
	};

	// Load from project or defaults
	function loadConfig(): AuthConfig {
		const cfg = projectCtx.project?.auth_config;
		if (!cfg || !cfg.providers) return { ...defaults };
		return {
			providers: cfg.providers ?? defaults.providers,
			password_min_length: cfg.password_min_length || defaults.password_min_length,
			require_email_confirmation: cfg.require_email_confirmation ?? defaults.require_email_confirmation,
			session_duration: cfg.session_duration || defaults.session_duration,
			redirect_urls: cfg.redirect_urls?.length ? cfg.redirect_urls : defaults.redirect_urls
		};
	}

	let emailPasswordEnabled = $state(true);
	let requireEmailConfirmation = $state(false);
	let passwordMinLength = $state(8);
	let sessionDuration = $state('168h');
	let redirectUrls = $state('http://localhost:3000');
	let saving = $state(false);
	let saveMessage = $state('');
	let saveError = $state('');

	const sessionOptions = [
		{ value: '1h', label: '1 hour' },
		{ value: '24h', label: '24 hours' },
		{ value: '168h', label: '7 days' },
		{ value: '720h', label: '30 days' }
	];

	$effect(() => {
		if (projectCtx.project) {
			const cfg = loadConfig();
			emailPasswordEnabled = cfg.providers?.email_password?.enabled ?? true;
			requireEmailConfirmation = cfg.require_email_confirmation;
			passwordMinLength = cfg.password_min_length;
			sessionDuration = cfg.session_duration;
			redirectUrls = cfg.redirect_urls.join('\n');
		}
	});

	async function handleSave() {
		saving = true;
		saveMessage = '';
		saveError = '';
		try {
			const config: AuthConfig = {
				providers: { email_password: { enabled: emailPasswordEnabled } },
				password_min_length: passwordMinLength,
				require_email_confirmation: requireEmailConfirmation,
				session_duration: sessionDuration,
				redirect_urls: redirectUrls.split('\n').map(u => u.trim()).filter(Boolean)
			};
			await api.updateProject(projectCtx.id, { auth_config: config });
			saveMessage = 'Auth configuration saved.';
			setTimeout(() => { saveMessage = ''; }, 3000);
		} catch (err) {
			saveError = err instanceof Error ? err.message : 'Failed to save';
		} finally {
			saving = false;
		}
	}
</script>

<svelte:head>
	<title>Auth - {projectCtx.project?.name ?? 'Project'} - Eurobase Console</title>
</svelte:head>

<div class="max-w-2xl">
	<h2 class="text-xl font-bold text-gray-900">Authentication</h2>
	<p class="mt-1 text-sm text-gray-500">Configure how your end-users authenticate.</p>

	{#if saveMessage}
		<div class="mt-4 rounded-lg bg-emerald-50 border border-emerald-200 px-4 py-3 text-sm text-emerald-700">
			{saveMessage}
		</div>
	{/if}

	{#if saveError}
		<div class="mt-4 rounded-lg bg-red-50 border border-red-200 px-4 py-3 text-sm text-red-700">
			{saveError}
		</div>
	{/if}

	<div class="mt-6 space-y-6">
		<!-- Auth Methods -->
		<div>
			<h3 class="text-sm font-semibold text-gray-900">Auth Methods</h3>
			<div class="mt-3 space-y-3">
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

				<div class="flex items-center justify-between rounded-lg border border-gray-200 px-4 py-3 opacity-50 cursor-not-allowed">
					<div>
						<p class="text-sm font-medium text-gray-900">Passkeys</p>
						<p class="text-xs text-gray-500">Passwordless auth with WebAuthn</p>
					</div>
					<span class="inline-flex items-center rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-500">Coming soon</span>
				</div>

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

				<div>
					<label for="redirect-urls" class="block text-sm font-medium text-gray-700">Allowed redirect URLs</label>
					<p class="text-xs text-gray-400 mt-0.5">One URL per line</p>
					<textarea
						id="redirect-urls"
						bind:value={redirectUrls}
						rows="3"
						class="mt-1.5 block w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm font-mono focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
					></textarea>
				</div>
			</div>
		</div>
	</div>

	<div class="mt-8">
		<button
			onclick={handleSave}
			disabled={saving}
			class="inline-flex items-center gap-2 rounded-lg bg-eurobase-600 px-5 py-2.5 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 transition-colors disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
		>
			{#if saving}
				<svg class="h-4 w-4 animate-spin" fill="none" viewBox="0 0 24 24">
					<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
					<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
				</svg>
				Saving...
			{:else}
				Save Changes
			{/if}
		</button>
	</div>
</div>
