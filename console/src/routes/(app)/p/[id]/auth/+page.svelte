<script lang="ts">
	import { getContext, onMount } from 'svelte';
	import { api, type AuthConfig, type EmailTemplate } from '$lib/api.js';

	const projectCtx: { id: string; project: import('$lib/api.js').Project | null } = getContext('projectId');

	// Tab state
	let activeTab = $state<'settings' | 'templates'>('settings');

	// Email status
	let emailConfigured = $state<boolean | null>(null);

	onMount(async () => {
		try {
			const status = await api.getEmailStatus();
			emailConfigured = status.configured;
		} catch {
			emailConfigured = false;
		}
	});

	// ---- Settings tab state ----

	const defaults: AuthConfig = {
		providers: { email_password: { enabled: true } },
		password_min_length: 8,
		require_email_confirmation: false,
		session_duration: '168h',
		redirect_urls: ['http://localhost:3000']
	};

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
	let magicLinkEnabled = $state(false);
	let requireEmailConfirmation = $state(false);
	let passwordMinLength = $state(8);
	let sessionDuration = $state('168h');
	let redirectUrls = $state('http://localhost:3000');
	let googleEnabled = $state(false);
	let googleClientId = $state('');
	let googleClientSecret = $state('');
	let googleSecretSet = $state(false);
	let googleSecretDirty = $state(false);
	let githubEnabled = $state(false);
	let githubClientId = $state('');
	let githubClientSecret = $state('');
	let githubSecretSet = $state(false);
	let githubSecretDirty = $state(false);
	let linkedinEnabled = $state(false);
	let linkedinClientId = $state('');
	let linkedinClientSecret = $state('');
	let linkedinSecretSet = $state(false);
	let linkedinSecretDirty = $state(false);
	let appleEnabled = $state(false);
	let appleClientId = $state('');
	let appleTeamId = $state('');
	let appleKeyId = $state('');
	let applePrivateKey = $state('');
	let appleSecretSet = $state(false);
	let appleSecretDirty = $state(false);
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
			magicLinkEnabled = cfg.providers?.magic_link?.enabled ?? false;
			requireEmailConfirmation = cfg.require_email_confirmation;
			passwordMinLength = cfg.password_min_length;
			sessionDuration = cfg.session_duration;
			redirectUrls = cfg.redirect_urls.join('\n');

			const oauthCfg = projectCtx.project?.auth_config?.oauth_providers;
			if (oauthCfg?.google) {
				googleEnabled = oauthCfg.google.enabled ?? false;
				googleClientId = oauthCfg.google.client_id ?? '';
				googleSecretSet = oauthCfg.google.secret_set ?? false;
				googleClientSecret = '';
				googleSecretDirty = false;
			}
			if (oauthCfg?.github) {
				githubEnabled = oauthCfg.github.enabled ?? false;
				githubClientId = oauthCfg.github.client_id ?? '';
				githubSecretSet = oauthCfg.github.secret_set ?? false;
				githubClientSecret = '';
				githubSecretDirty = false;
			}
			if (oauthCfg?.linkedin) {
				linkedinEnabled = oauthCfg.linkedin.enabled ?? false;
				linkedinClientId = oauthCfg.linkedin.client_id ?? '';
				linkedinSecretSet = oauthCfg.linkedin.secret_set ?? false;
				linkedinClientSecret = '';
				linkedinSecretDirty = false;
			}
			if (oauthCfg?.apple) {
				appleEnabled = oauthCfg.apple.enabled ?? false;
				appleClientId = oauthCfg.apple.client_id ?? '';
				appleTeamId = oauthCfg.apple.team_id ?? '';
				appleKeyId = oauthCfg.apple.key_id ?? '';
				appleSecretSet = oauthCfg.apple.secret_set ?? false;
				applePrivateKey = '';
				appleSecretDirty = false;
			}
		}
	});

	async function handleSave() {
		saving = true;
		saveMessage = '';
		saveError = '';
		try {
			// Only send client_secret when the user actually typed a new value —
			// otherwise the backend leaves whatever is already in the vault alone.
			const googleProvider: Record<string, any> = {
				enabled: googleEnabled,
				client_id: googleClientId
			};
			if (googleSecretDirty && googleClientSecret) {
				googleProvider.client_secret = googleClientSecret;
			}
			const githubProvider: Record<string, any> = {
				enabled: githubEnabled,
				client_id: githubClientId
			};
			if (githubSecretDirty && githubClientSecret) {
				githubProvider.client_secret = githubClientSecret;
			}

			const linkedinProvider: Record<string, any> = {
				enabled: linkedinEnabled,
				client_id: linkedinClientId
			};
			if (linkedinSecretDirty && linkedinClientSecret) {
				linkedinProvider.client_secret = linkedinClientSecret;
			}
			const appleProvider: Record<string, any> = {
				enabled: appleEnabled,
				client_id: appleClientId,
				team_id: appleTeamId,
				key_id: appleKeyId
			};
			if (appleSecretDirty && applePrivateKey) {
				appleProvider.client_secret = applePrivateKey;
			}

			const config: AuthConfig = {
				providers: { email_password: { enabled: emailPasswordEnabled }, magic_link: { enabled: magicLinkEnabled } },
				oauth_providers: {
					google: googleProvider as any,
					github: githubProvider as any,
					linkedin: linkedinProvider as any,
					apple: appleProvider as any
				},
				password_min_length: passwordMinLength,
				require_email_confirmation: requireEmailConfirmation,
				session_duration: sessionDuration,
				redirect_urls: redirectUrls.split('\n').map(u => u.trim()).filter(Boolean)
			};
			const updated = await api.updateProject(projectCtx.id, { auth_config: config });

			// Refresh secret_set from the server response so the UI reflects the
			// new state (and clear the form fields so the user doesn't see their
			// just-entered secret lingering).
			const oauthCfg = updated?.auth_config?.oauth_providers;
			if (oauthCfg?.google) {
				googleSecretSet = oauthCfg.google.secret_set ?? googleSecretSet;
			}
			if (oauthCfg?.github) {
				githubSecretSet = oauthCfg.github.secret_set ?? githubSecretSet;
			}
			if (oauthCfg?.linkedin) {
				linkedinSecretSet = oauthCfg.linkedin.secret_set ?? linkedinSecretSet;
			}
			if (oauthCfg?.apple) {
				appleSecretSet = oauthCfg.apple.secret_set ?? appleSecretSet;
			}
			googleClientSecret = '';
			googleSecretDirty = false;
			githubClientSecret = '';
			githubSecretDirty = false;
			linkedinClientSecret = '';
			linkedinSecretDirty = false;
			applePrivateKey = '';
			appleSecretDirty = false;

			saveMessage = 'Auth configuration saved.';
			setTimeout(() => { saveMessage = ''; }, 3000);
		} catch (err) {
			saveError = err instanceof Error ? err.message : 'Failed to save';
		} finally {
			saving = false;
		}
	}

	// ---- Templates tab state ----

	const templateTypes = [
		{ type: 'verification', label: 'Email Verification' },
		{ type: 'password_reset', label: 'Password Reset' },
		{ type: 'magic_link', label: 'Magic Link' },
		{ type: 'welcome', label: 'Welcome' },
		{ type: 'password_changed', label: 'Password Changed' }
	];

	const templateVars = ['{{.UserEmail}}', '{{.ProjectName}}', '{{.ActionURL}}', '{{.ExpiresIn}}'];

	let templates = $state<EmailTemplate[]>([]);
	let templatesLoading = $state(false);
	let editingType = $state<string | null>(null);
	let editSubject = $state('');
	let editBodyHtml = $state('');
	let templateSaving = $state(false);
	let templateMessage = $state('');
	let templateError = $state('');
	let previewHtml = $state('');
	let previewSubject = $state('');
	let testSendingType = $state<string | null>(null);

	async function loadTemplates() {
		templatesLoading = true;
		try {
			templates = await api.listEmailTemplates(projectCtx.id);
		} catch (err) {
			templateError = err instanceof Error ? err.message : 'Failed to load templates';
		} finally {
			templatesLoading = false;
		}
	}

	function startEditing(tmpl: EmailTemplate) {
		editingType = tmpl.template_type;
		editSubject = tmpl.subject;
		editBodyHtml = tmpl.body_html;
		previewHtml = '';
		previewSubject = '';
		templateMessage = '';
		templateError = '';
	}

	function cancelEditing() {
		editingType = null;
		previewHtml = '';
		previewSubject = '';
	}

	async function saveTemplate() {
		if (!editingType) return;
		templateSaving = true;
		templateMessage = '';
		templateError = '';
		try {
			await api.updateEmailTemplate(projectCtx.id, editingType, {
				subject: editSubject,
				body_html: editBodyHtml
			});
			templateMessage = 'Template saved.';
			setTimeout(() => { templateMessage = ''; }, 3000);
			await loadTemplates();
			editingType = null;
		} catch (err) {
			templateError = err instanceof Error ? err.message : 'Failed to save template';
		} finally {
			templateSaving = false;
		}
	}

	async function resetTemplate(type: string) {
		templateMessage = '';
		templateError = '';
		try {
			await api.deleteEmailTemplate(projectCtx.id, type);
			templateMessage = 'Template reset to default.';
			setTimeout(() => { templateMessage = ''; }, 3000);
			await loadTemplates();
			if (editingType === type) editingType = null;
		} catch (err) {
			templateError = err instanceof Error ? err.message : 'Failed to reset template';
		}
	}

	async function previewTemplate() {
		if (!editingType) return;
		try {
			const result = await api.previewEmailTemplate(projectCtx.id, editingType, {
				subject: editSubject,
				body_html: editBodyHtml
			});
			previewSubject = result.subject;
			previewHtml = result.body;
		} catch (err) {
			templateError = err instanceof Error ? err.message : 'Preview failed';
		}
	}

	async function sendTestEmail(type: string) {
		testSendingType = type;
		templateMessage = '';
		templateError = '';
		try {
			const result = await api.testEmailTemplate(projectCtx.id, type);
			templateMessage = `Test email sent to ${result.sent_to}`;
			setTimeout(() => { templateMessage = ''; }, 5000);
		} catch (err) {
			templateError = err instanceof Error ? err.message : 'Failed to send test email';
		} finally {
			testSendingType = null;
		}
	}

	// Load templates when switching to templates tab
	$effect(() => {
		if (activeTab === 'templates' && templates.length === 0) {
			loadTemplates();
		}
	});
</script>

<svelte:head>
	<title>Auth - {projectCtx.project?.name ?? 'Project'} - Eurobase Console</title>
</svelte:head>

<div class="max-w-3xl">
	<h2 class="text-xl font-bold text-gray-900">Authentication</h2>
	<p class="mt-1 text-sm text-gray-500">Configure how your end-users authenticate.</p>

	<!-- Email status banner -->
	{#if emailConfigured === false}
		<div class="mt-4 rounded-lg bg-amber-50 border border-amber-200 px-4 py-3 text-sm text-amber-700">
			Configure Scaleway TEM environment variables to enable email features (verification, password reset).
		</div>
	{/if}

	<!-- Tab navigation -->
	<div class="mt-6 border-b border-gray-200">
		<nav class="-mb-px flex gap-6">
			<button
				onclick={() => activeTab = 'settings'}
				class="pb-3 text-sm font-medium border-b-2 transition-colors cursor-pointer {activeTab === 'settings' ? 'border-eurobase-600 text-eurobase-600' : 'border-transparent text-gray-500 hover:text-gray-700 hover:border-gray-300'}"
			>
				Settings
			</button>
			<button
				onclick={() => activeTab = 'templates'}
				class="pb-3 text-sm font-medium border-b-2 transition-colors cursor-pointer {activeTab === 'templates' ? 'border-eurobase-600 text-eurobase-600' : 'border-transparent text-gray-500 hover:text-gray-700 hover:border-gray-300'}"
			>
				Email Templates
			</button>
		</nav>
	</div>

	{#if saveMessage || templateMessage}
		<div class="mt-4 rounded-lg bg-emerald-50 border border-emerald-200 px-4 py-3 text-sm text-emerald-700">
			{saveMessage || templateMessage}
		</div>
	{/if}

	{#if saveError || templateError}
		<div class="mt-4 rounded-lg bg-red-50 border border-red-200 px-4 py-3 text-sm text-red-700">
			{saveError || templateError}
		</div>
	{/if}

	<!-- Settings Tab -->
	{#if activeTab === 'settings'}
		<div class="mt-6 space-y-6">
			<!-- Auth Methods -->
			<div>
				<h3 class="text-sm font-semibold text-gray-900">Auth Methods</h3>
				<div class="mt-3 space-y-3">
					<div class="rounded-lg border border-gray-200 px-4 py-3">
						<div class="flex items-center justify-between">
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
						{#if emailPasswordEnabled}
							<div class="mt-3 space-y-3 border-t border-gray-100 pt-3">
								<div class="flex items-start justify-between">
									<div>
										<p class="text-xs font-medium text-gray-700">Require email confirmation</p>
										{#if emailConfigured === false}
											<p class="text-[10px] text-amber-500 mt-0.5">Requires email sending to be configured</p>
										{:else}
											<p class="text-[10px] text-gray-400 mt-0.5">Users must verify their email before signing in</p>
										{/if}
									</div>
									<button
										type="button"
										role="switch"
										aria-checked={requireEmailConfirmation}
										onclick={() => requireEmailConfirmation = !requireEmailConfirmation}
										class="relative inline-flex h-5 w-9 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 {requireEmailConfirmation ? 'bg-eurobase-600' : 'bg-gray-200'}"
									>
										<span class="pointer-events-none inline-block h-4 w-4 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out {requireEmailConfirmation ? 'translate-x-4' : 'translate-x-0'}"></span>
									</button>
								</div>
								<div>
									<label for="pwd-min" class="block text-xs font-medium text-gray-700">Minimum password length</label>
									<input
										id="pwd-min"
										type="number"
										min="8"
										max="128"
										bind:value={passwordMinLength}
										class="mt-1 block w-20 rounded-lg border border-gray-300 px-2.5 py-1.5 text-xs text-gray-900 shadow-sm focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
									/>
								</div>
							</div>
						{/if}
					</div>

					<div class="rounded-lg border border-gray-200 px-4 py-3">
						<div class="flex items-center justify-between">
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
						{#if magicLinkEnabled}
							<div class="mt-3 rounded-lg bg-eurobase-50 border border-eurobase-100 p-3 space-y-2">
								<p class="text-xs font-medium text-eurobase-800">How Magic Links work</p>
								<p class="text-xs text-eurobase-700 leading-relaxed">Users enter their email and receive a sign-in link. Clicking the link signs them in instantly — no password needed. Links expire after 15 minutes and can only be used once. Email is automatically verified on first use.</p>
								<p class="text-xs font-medium text-eurobase-800 mt-2">SDK usage</p>
								<div class="rounded-md bg-gray-900 p-2.5 font-mono text-[11px] text-green-400 leading-relaxed overflow-x-auto">
									<div class="text-gray-500">// 1. Request a magic link email</div>
									<div>await eb.auth.requestMagicLink('user@example.com')</div>
									<div class="mt-2 text-gray-500">// 2. User clicks email link → your app receives the token</div>
									<div class="text-gray-500">// Extract from URL: /auth/callback?token=abc123</div>
									<div>const token = new URL(location.href).searchParams.get('token')</div>
									<div class="mt-2 text-gray-500">// 3. Exchange token for session</div>
									<div>const {"{"} data, error {"}"} = await eb.auth.signInWithMagicLink(token)</div>
								</div>
								<p class="text-xs font-medium text-eurobase-800 mt-2">REST API</p>
								<div class="rounded-md bg-gray-900 p-2.5 font-mono text-[11px] text-green-400 leading-relaxed overflow-x-auto">
									<div><span class="text-amber-400">POST</span> /v1/auth/request-magic-link</div>
									<div class="text-gray-400">Body: {"{"}"email": "user@example.com"{"}"}</div>
									<div class="mt-1.5"><span class="text-amber-400">POST</span> /v1/auth/signin-magic-link</div>
									<div class="text-gray-400">Body: {"{"}"token": "abc123..."{"}"}</div>
								</div>
								<p class="text-[11px] text-eurobase-600 mt-1">Customize the email template in the Email Templates tab.</p>
							</div>
						{/if}
					</div>

					<div class="flex items-center justify-between rounded-lg border border-gray-200 px-4 py-3 opacity-50 cursor-not-allowed">
						<div>
							<p class="text-sm font-medium text-gray-900">Passkeys</p>
							<p class="text-xs text-gray-500">Passwordless auth with WebAuthn</p>
						</div>
						<span class="inline-flex items-center rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-500">Coming soon</span>
					</div>

					<!-- Google OAuth -->
					<div class="rounded-lg border border-gray-200 px-4 py-3">
						<div class="flex items-center justify-between">
							<div>
								<p class="text-sm font-medium text-gray-900">Google</p>
								<p class="text-xs text-gray-500">Sign in with Google account</p>
							</div>
							<button
								type="button"
								role="switch"
								aria-checked={googleEnabled}
								onclick={() => googleEnabled = !googleEnabled}
								class="relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 {googleEnabled ? 'bg-eurobase-600' : 'bg-gray-200'}"
							>
								<span class="pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out {googleEnabled ? 'translate-x-5' : 'translate-x-0'}"></span>
							</button>
						</div>
						{#if googleEnabled}
							<div class="mt-3 space-y-3">
								<div>
									<label for="google-client-id" class="block text-xs font-medium text-gray-700">Client ID</label>
									<input id="google-client-id" type="text" bind:value={googleClientId} placeholder="123456789.apps.googleusercontent.com" class="mt-1 block w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors" />
								</div>
								<div>
									<label for="google-client-secret" class="block text-xs font-medium text-gray-700">
										Client Secret
										{#if googleSecretSet && !googleSecretDirty}
											<span class="ml-2 inline-flex items-center gap-1 rounded-full bg-green-50 px-2 py-0.5 text-[10px] font-medium text-green-700 border border-green-200">
												<svg class="h-3 w-3" fill="none" viewBox="0 0 24 24" stroke-width="2.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
												Encrypted in vault
											</span>
										{/if}
									</label>
									<input
										id="google-client-secret"
										type="password"
										bind:value={googleClientSecret}
										oninput={() => googleSecretDirty = true}
										placeholder={googleSecretSet ? '•••••••••••••• (leave blank to keep current)' : 'GOCSPX-...'}
										class="mt-1 block w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
									/>
								</div>
								<div class="rounded-lg bg-eurobase-50 border border-eurobase-100 p-3">
									<p class="text-xs font-medium text-eurobase-800">Setup instructions</p>
									<ol class="mt-1 text-xs text-eurobase-700 list-decimal list-inside space-y-1">
										<li>Go to <a href="https://console.cloud.google.com/apis/credentials" target="_blank" rel="noopener" class="underline">Google Cloud Console</a></li>
										<li>Create OAuth 2.0 credentials (Web application)</li>
										<li>Add your Eurobase API URL + <code class="bg-eurobase-100 px-1 rounded">/v1/auth/oauth/google/callback</code> as an authorized redirect URI</li>
										<li>Copy the Client ID and Client Secret here</li>
									</ol>
								</div>
							</div>
						{/if}
					</div>

					<!-- GitHub OAuth -->
					<div class="rounded-lg border border-gray-200 px-4 py-3">
						<div class="flex items-center justify-between">
							<div>
								<p class="text-sm font-medium text-gray-900">GitHub</p>
								<p class="text-xs text-gray-500">Sign in with GitHub account</p>
							</div>
							<button
								type="button"
								role="switch"
								aria-checked={githubEnabled}
								onclick={() => githubEnabled = !githubEnabled}
								class="relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 {githubEnabled ? 'bg-eurobase-600' : 'bg-gray-200'}"
							>
								<span class="pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out {githubEnabled ? 'translate-x-5' : 'translate-x-0'}"></span>
							</button>
						</div>
						{#if githubEnabled}
							<div class="mt-3 space-y-3">
								<div>
									<label for="github-client-id" class="block text-xs font-medium text-gray-700">Client ID</label>
									<input id="github-client-id" type="text" bind:value={githubClientId} placeholder="Iv1.abc123..." class="mt-1 block w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors" />
								</div>
								<div>
									<label for="github-client-secret" class="block text-xs font-medium text-gray-700">
										Client Secret
										{#if githubSecretSet && !githubSecretDirty}
											<span class="ml-2 inline-flex items-center gap-1 rounded-full bg-green-50 px-2 py-0.5 text-[10px] font-medium text-green-700 border border-green-200">
												<svg class="h-3 w-3" fill="none" viewBox="0 0 24 24" stroke-width="2.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
												Encrypted in vault
											</span>
										{/if}
									</label>
									<input
										id="github-client-secret"
										type="password"
										bind:value={githubClientSecret}
										oninput={() => githubSecretDirty = true}
										placeholder={githubSecretSet ? '•••••••••••••• (leave blank to keep current)' : 'secret_...'}
										class="mt-1 block w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
									/>
								</div>
								<div class="rounded-lg bg-eurobase-50 border border-eurobase-100 p-3">
									<p class="text-xs font-medium text-eurobase-800">Setup instructions</p>
									<ol class="mt-1 text-xs text-eurobase-700 list-decimal list-inside space-y-1">
										<li>Go to <a href="https://github.com/settings/developers" target="_blank" rel="noopener" class="underline">GitHub Developer Settings</a></li>
										<li>Create a new OAuth App</li>
										<li>Set Authorization callback URL to your Eurobase API URL + <code class="bg-eurobase-100 px-1 rounded">/v1/auth/oauth/github/callback</code></li>
										<li>Copy the Client ID and Client Secret here</li>
									</ol>
								</div>
							</div>
						{/if}
					</div>

					<!-- LinkedIn OAuth -->
					<div class="rounded-lg border border-gray-200 px-4 py-3">
						<div class="flex items-center justify-between">
							<div>
								<p class="text-sm font-medium text-gray-900">LinkedIn</p>
								<p class="text-xs text-gray-500">Sign in with LinkedIn account</p>
							</div>
							<button
								type="button"
								role="switch"
								aria-checked={linkedinEnabled}
								onclick={() => linkedinEnabled = !linkedinEnabled}
								class="relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 {linkedinEnabled ? 'bg-eurobase-600' : 'bg-gray-200'}"
							>
								<span class="pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out {linkedinEnabled ? 'translate-x-5' : 'translate-x-0'}"></span>
							</button>
						</div>
						{#if linkedinEnabled}
							<div class="mt-3 space-y-3">
								<div>
									<label for="linkedin-client-id" class="block text-xs font-medium text-gray-700">Client ID</label>
									<input id="linkedin-client-id" type="text" bind:value={linkedinClientId} placeholder="77abc123def456" class="mt-1 block w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors" />
								</div>
								<div>
									<label for="linkedin-client-secret" class="block text-xs font-medium text-gray-700">
										Client Secret
										{#if linkedinSecretSet && !linkedinSecretDirty}
											<span class="ml-2 inline-flex items-center gap-1 rounded-full bg-green-50 px-2 py-0.5 text-[10px] font-medium text-green-700 border border-green-200">
												<svg class="h-3 w-3" fill="none" viewBox="0 0 24 24" stroke-width="2.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
												Encrypted in vault
											</span>
										{/if}
									</label>
									<input
										id="linkedin-client-secret"
										type="password"
										bind:value={linkedinClientSecret}
										oninput={() => linkedinSecretDirty = true}
										placeholder={linkedinSecretSet ? '•••••••••••••• (leave blank to keep current)' : 'secret_...'}
										class="mt-1 block w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
									/>
								</div>
								<div class="rounded-lg bg-eurobase-50 border border-eurobase-100 p-3">
									<p class="text-xs font-medium text-eurobase-800">Setup instructions</p>
									<ol class="mt-1 text-xs text-eurobase-700 list-decimal list-inside space-y-1">
										<li>Go to <a href="https://www.linkedin.com/developers/apps" target="_blank" rel="noopener" class="underline">LinkedIn Developer Portal</a></li>
										<li>Create a new app and request the "Sign In with LinkedIn using OpenID Connect" product</li>
										<li>Under OAuth 2.0 settings, add your Eurobase API URL + <code class="bg-eurobase-100 px-1 rounded">/v1/auth/oauth/linkedin/callback</code> as an authorized redirect URL</li>
										<li>Copy the Client ID and Client Secret here</li>
									</ol>
								</div>
							</div>
						{/if}
					</div>

					<!-- Apple OAuth -->
					<div class="rounded-lg border border-gray-200 px-4 py-3">
						<div class="flex items-center justify-between">
							<div>
								<p class="text-sm font-medium text-gray-900">Apple</p>
								<p class="text-xs text-gray-500">Sign in with Apple</p>
							</div>
							<button
								type="button"
								role="switch"
								aria-checked={appleEnabled}
								onclick={() => appleEnabled = !appleEnabled}
								class="relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 {appleEnabled ? 'bg-eurobase-600' : 'bg-gray-200'}"
							>
								<span class="pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out {appleEnabled ? 'translate-x-5' : 'translate-x-0'}"></span>
							</button>
						</div>
						{#if appleEnabled}
							<div class="mt-3 space-y-3">
								<div>
									<label for="apple-client-id" class="block text-xs font-medium text-gray-700">Service ID (Client ID)</label>
									<input id="apple-client-id" type="text" bind:value={appleClientId} placeholder="com.example.myapp" class="mt-1 block w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors" />
								</div>
								<div>
									<label for="apple-team-id" class="block text-xs font-medium text-gray-700">Team ID</label>
									<input id="apple-team-id" type="text" bind:value={appleTeamId} placeholder="ABCDE12345" class="mt-1 block w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors" />
								</div>
								<div>
									<label for="apple-key-id" class="block text-xs font-medium text-gray-700">Key ID</label>
									<input id="apple-key-id" type="text" bind:value={appleKeyId} placeholder="ABC123DEFG" class="mt-1 block w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors" />
								</div>
								<div>
									<label for="apple-private-key" class="block text-xs font-medium text-gray-700">
										Private Key (ES256 .p8 file contents)
										{#if appleSecretSet && !appleSecretDirty}
											<span class="ml-2 inline-flex items-center gap-1 rounded-full bg-green-50 px-2 py-0.5 text-[10px] font-medium text-green-700 border border-green-200">
												<svg class="h-3 w-3" fill="none" viewBox="0 0 24 24" stroke-width="2.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
												Encrypted in vault
											</span>
										{/if}
									</label>
									<textarea
										id="apple-private-key"
										bind:value={applePrivateKey}
										oninput={() => appleSecretDirty = true}
										rows="4"
										placeholder={appleSecretSet ? '•••••••••••••• (leave blank to keep current)' : '-----BEGIN PRIVATE KEY-----\n...\n-----END PRIVATE KEY-----'}
										class="mt-1 block w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm font-mono focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
									></textarea>
								</div>
								<div class="rounded-lg bg-eurobase-50 border border-eurobase-100 p-3">
									<p class="text-xs font-medium text-eurobase-800">Setup instructions</p>
									<ol class="mt-1 text-xs text-eurobase-700 list-decimal list-inside space-y-1">
										<li>Go to <a href="https://developer.apple.com/account/resources" target="_blank" rel="noopener" class="underline">Apple Developer Portal</a></li>
										<li>Register a Services ID (this is the Client ID)</li>
										<li>Enable "Sign In with Apple" and configure the return URL: your Eurobase API URL + <code class="bg-eurobase-100 px-1 rounded">/v1/auth/oauth/apple/callback</code></li>
										<li>Create a private key for Sign In with Apple — download the .p8 file</li>
										<li>Copy your Team ID (top-right of developer portal), the Key ID, and paste the private key contents here</li>
									</ol>
								</div>
								<div class="rounded-lg bg-amber-50 border border-amber-200 p-3">
									<p class="text-xs text-amber-700">Apple only sends the user's name on first authorization. If the user has already authorized your app, their name may not appear in Eurobase. Apple may also use a private relay email address.</p>
								</div>
							</div>
						{/if}
					</div>

					{#if googleEnabled || githubEnabled || linkedinEnabled || appleEnabled}
						<div class="rounded-lg bg-blue-50 border border-blue-200 px-4 py-3">
							<p class="text-xs text-blue-700">Social login uses third-party providers only to verify user identity. No application data is shared. All user records remain in EU infrastructure.</p>
						</div>
					{/if}
				</div>
			</div>

			<!-- Settings -->
			<div>
				<h3 class="text-sm font-semibold text-gray-900">Session Settings</h3>
				<div class="mt-3 space-y-4">
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
						<p class="text-xs text-gray-400 mt-0.5">URLs where users can be redirected after authentication (e.g. email verification, password reset, OAuth callbacks). Add one URL per line. Include both your development and production URLs. Must include scheme and host (e.g. https://myapp.com/callback).</p>
						<textarea
							id="redirect-urls"
							bind:value={redirectUrls}
							rows="3"
							placeholder="http://localhost:3000&#10;https://myapp.com"
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
	{/if}

	<!-- Email Templates Tab -->
	{#if activeTab === 'templates'}
		<div class="mt-6 space-y-4">
			{#if templatesLoading}
				<p class="text-sm text-gray-500">Loading templates...</p>
			{:else if editingType}
				<!-- Template editor -->
				{@const typeLabel = templateTypes.find(t => t.type === editingType)?.label ?? editingType}
				<div class="space-y-4">
					<div class="flex items-center justify-between">
						<h3 class="text-sm font-semibold text-gray-900">Editing: {typeLabel}</h3>
						<button onclick={cancelEditing} class="text-sm text-gray-500 hover:text-gray-700 cursor-pointer">Cancel</button>
					</div>

					<!-- Variable reference -->
					<div class="rounded-lg bg-gray-50 border border-gray-200 px-4 py-3">
						<p class="text-xs font-medium text-gray-600 mb-2">Template variables — automatically replaced when the email is sent (click to copy):</p>
						<div class="flex flex-wrap gap-2 mb-2">
							{#each templateVars as v}
								<button
									type="button"
									onclick={() => { navigator.clipboard.writeText(v); }}
									title="Click to copy"
									class="text-xs bg-white border border-gray-200 rounded px-2 py-0.5 text-gray-700 font-mono cursor-pointer hover:bg-eurobase-50 hover:border-eurobase-300 transition-colors"
								>{v}</button>
							{/each}
						</div>
						<div class="text-[11px] text-gray-400 space-y-0.5">
							<p><strong class="text-gray-500">UserEmail</strong> — the end-user's email address</p>
							<p><strong class="text-gray-500">ProjectName</strong> — your project name</p>
							<p><strong class="text-gray-500">ActionURL</strong> — the verification or reset link (auto-generated with token)</p>
							<p><strong class="text-gray-500">ExpiresIn</strong> — how long the link is valid (e.g. "24 hours")</p>
						</div>
					</div>

					<div>
						<label for="tpl-subject" class="block text-sm font-medium text-gray-700">Subject</label>
						<input
							id="tpl-subject"
							type="text"
							bind:value={editSubject}
							class="mt-1 block w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
						/>
					</div>

					<div>
						<label for="tpl-body" class="block text-sm font-medium text-gray-700">HTML Body</label>
						<textarea
							id="tpl-body"
							bind:value={editBodyHtml}
							rows="16"
							class="mt-1 block w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm font-mono bg-gray-900 text-green-400 focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
						></textarea>
					</div>

					<div class="flex items-center gap-3">
						<button
							onclick={saveTemplate}
							disabled={templateSaving}
							class="inline-flex items-center gap-2 rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 transition-colors disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
						>
							{templateSaving ? 'Saving...' : 'Save Template'}
						</button>
						<button
							onclick={previewTemplate}
							class="inline-flex items-center rounded-lg border border-gray-300 px-4 py-2 text-sm font-medium text-gray-700 shadow-sm hover:bg-gray-50 transition-colors cursor-pointer"
						>
							Preview
						</button>
					</div>

					{#if previewHtml}
						<div class="space-y-2">
							<h4 class="text-sm font-medium text-gray-700">Preview</h4>
							<p class="text-xs text-gray-500">Subject: {previewSubject}</p>
							<div class="rounded-lg border border-gray-200 overflow-hidden">
								{@html previewHtml}
							</div>
						</div>
					{/if}
				</div>
			{:else}
				<!-- Template list -->
				{#each templateTypes as tt}
					{@const tmpl = templates.find(t => t.template_type === tt.type)}
					<div class="rounded-lg border border-gray-200 px-4 py-4">
						<div class="flex items-start justify-between">
							<div>
								<p class="text-sm font-medium text-gray-900">{tt.label}</p>
								<p class="text-xs text-gray-500 mt-0.5">
									{tmpl?.is_custom ? 'Custom template' : 'Default template'}
								</p>
								{#if tmpl}
									<p class="text-xs text-gray-400 mt-1">Subject: {tmpl.subject}</p>
								{/if}
							</div>
							<div class="flex items-center gap-2">
								{#if emailConfigured}
									<button
										onclick={() => sendTestEmail(tt.type)}
										disabled={testSendingType !== null}
										title="Sends test email to your account email"
										class="text-xs text-gray-500 hover:text-gray-700 cursor-pointer disabled:opacity-50"
									>
										{testSendingType === tt.type ? 'Sending...' : 'Send Test'}
									</button>
								{/if}
								{#if tmpl?.is_custom}
									<button
										onclick={() => resetTemplate(tt.type)}
										class="text-xs text-red-500 hover:text-red-700 cursor-pointer"
									>
										Reset
									</button>
								{/if}
								<button
									onclick={() => tmpl && startEditing(tmpl)}
									class="text-xs text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer"
								>
									Edit
								</button>
							</div>
						</div>
					</div>
				{/each}
			{/if}
		</div>
	{/if}
</div>
