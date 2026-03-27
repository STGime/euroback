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
		}
	});

	async function handleSave() {
		saving = true;
		saveMessage = '';
		saveError = '';
		try {
			const config: AuthConfig = {
				providers: { email_password: { enabled: emailPasswordEnabled }, magic_link: { enabled: magicLinkEnabled } },
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
								<p class="text-[11px] text-eurobase-600 mt-1">Requires Scaleway TEM email to be configured. Customize the email template in the Email Templates tab.</p>
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
							{#if emailConfigured === false}
								<p class="text-xs text-amber-500 mt-0.5">Requires Scaleway TEM configuration</p>
							{:else}
								<p class="text-xs text-gray-400 mt-0.5">Users must verify their email before signing in</p>
							{/if}
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
