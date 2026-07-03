<script lang="ts">
	import { getContext, onMount } from 'svelte';
	import { api, DEFAULT_RATE_LIMITS, type AuthConfig, type EmailTemplate, type ProjectEmailSender, type RateLimits } from '$lib/api.js';

	const projectCtx: { id: string; project: import('$lib/api.js').Project | null; updateProject: (p: import('$lib/api.js').Project) => void } = getContext('projectId');

	// Tab state
	let activeTab = $state<'settings' | 'templates' | 'rate_limits' | 'smtp'>('settings');

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
		redirect_urls: ['http://localhost:3000'],
		cors_origins: []
	};

	function loadConfig(): AuthConfig {
		const cfg = projectCtx.project?.auth_config;
		if (!cfg || !cfg.providers) return { ...defaults };
		return {
			providers: cfg.providers ?? defaults.providers,
			password_min_length: cfg.password_min_length || defaults.password_min_length,
			require_email_confirmation: cfg.require_email_confirmation ?? defaults.require_email_confirmation,
			session_duration: cfg.session_duration || defaults.session_duration,
			redirect_urls: cfg.redirect_urls?.length ? cfg.redirect_urls : defaults.redirect_urls,
			cors_origins: cfg.cors_origins ?? []
		};
	}

	let emailPasswordEnabled = $state(true);
	let magicLinkEnabled = $state(false);
	let phoneEnabled = $state(false);
	let requireEmailConfirmation = $state(false);
	let passwordMinLength = $state(8);
	let sessionDuration = $state('168h');
	let redirectUrls = $state('http://localhost:3000');
	let corsOrigins = $state('');
	// #260 email-flow redirect URLs (part of #257). Each must be a member
	// of the redirect_urls list above or the backend rejects PATCH.
	let emailVerificationUrl = $state('');
	let passwordResetUrl = $state('');
	let magicLinkUrl = $state('');
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
	let microsoftEnabled = $state(false);
	let microsoftClientId = $state('');
	let microsoftClientSecret = $state('');
	let microsoftTenantId = $state('');
	let microsoftSecretSet = $state(false);
	let microsoftSecretDirty = $state(false);
	let saving = $state(false);
	let saveMessage = $state('');
	let saveError = $state('');

	// ---- Rate Limits tab state (#229, umbrella #224) ----
	//
	// The five numeric knobs are kept as strings so an empty input maps
	// cleanly to "use platform default" — saving an empty value omits
	// the field from the payload, and the backend's
	// EffectiveRateLimits merge fills in the default. Placeholder text
	// shows the current default so the form is self-explanatory.
	//
	// trust_proxy is a plain bool toggle; saving always persists an
	// explicit value (the project owner has "chosen" once they touch
	// the page). A future "Reset to defaults" button could clear the
	// override; out of scope for #229.
	let rlEmailsPerHour = $state('');
	let rlSmsPerHour = $state('');
	let rlTokenRefresh = $state('');
	let rlTokenVerify = $state('');
	let rlSignupSignin = $state('');
	let rlTrustProxy = $state<boolean>(DEFAULT_RATE_LIMITS.trust_proxy);
	// `rlTrustProxyTouched` distinguishes "the user clicked the toggle
	// this session" from "the toggle reflects the saved value". Numeric
	// fields use empty-string-means-default as their "absent" signal; a
	// bool toggle has no equivalent representation, so we use a sidecar
	// flag. Without it, every save would persist an explicit `false`
	// for trust_proxy — silently opting the project out of any future
	// platform-default flip (the *bool semantic in #237 / #238 exists
	// specifically to distinguish "absent → use default" from "explicit
	// false → stay false even if the default changes").
	let rlTrustProxyTouched = $state(false);
	let rlSaving = $state(false);
	let rlSaveMessage = $state('');
	let rlSaveError = $state('');

	// ---- SMTP tab state (#235 Part 1, BYO custom SMTP) ----
	//
	// We keep `existing` separate from the form state so the
	// form bind:value can mutate freely without losing the
	// "what's saved" view (verified_at, last_error, has_password
	// indicator). hydrateSmtp populates both on load.
	//
	// `smtpPassword` is treated specially:
	//   - blank + existing.has_password → backend preserves the
	//     stored sealed bytes (no re-prompt needed for edits)
	//   - blank + no existing password   → sealed columns stay NULL
	//   - non-blank                       → backend re-seals
	let smtpLoading = $state(true);
	let smtpExisting = $state<ProjectEmailSender | null>(null);
	let smtpHost = $state('');
	let smtpPort = $state('587');
	let smtpUsername = $state('');
	let smtpFromEmail = $state('');
	let smtpFromName = $state('');
	let smtpEncryption = $state<'starttls' | 'tls' | 'none'>('starttls');
	let smtpPassword = $state('');
	let smtpSaving = $state(false);
	let smtpSaveMessage = $state('');
	let smtpSaveError = $state('');
	let smtpTestTo = $state('');
	let smtpTesting = $state(false);
	let smtpTestError = $state('');
	let smtpTestMessage = $state('');

	async function loadSmtp() {
		smtpLoading = true;
		smtpSaveError = '';
		smtpTestError = '';
		try {
			const s = await api.getProjectEmailSender(projectCtx.id);
			hydrateSmtp(s);
		} catch (err) {
			smtpSaveError = err instanceof Error ? err.message : 'Failed to load SMTP config';
		} finally {
			smtpLoading = false;
		}
	}

	function hydrateSmtp(s: ProjectEmailSender | null) {
		smtpExisting = s;
		smtpHost = s?.host ?? '';
		smtpPort = s ? String(s.port) : '587';
		smtpUsername = s?.username ?? '';
		smtpFromEmail = s?.from_email ?? '';
		smtpFromName = s?.from_name ?? '';
		smtpEncryption = s?.encryption ?? 'starttls';
		smtpPassword = '';
		// Default the test-to field to the from_email so a single
		// click verifies the round-trip without further typing.
		smtpTestTo = s?.from_email ?? '';
	}

	async function handleSaveSmtp() {
		smtpSaving = true;
		smtpSaveError = '';
		smtpSaveMessage = '';
		try {
			const portNum = parseInt(smtpPort, 10);
			if (Number.isNaN(portNum)) {
				smtpSaveError = 'Port must be a number';
				return;
			}
			const updated = await api.upsertProjectEmailSender(projectCtx.id, {
				host: smtpHost.trim(),
				port: portNum,
				username: smtpUsername.trim(),
				from_email: smtpFromEmail.trim(),
				from_name: smtpFromName.trim(),
				encryption: smtpEncryption,
				password: smtpPassword
			});
			hydrateSmtp(updated);
			smtpSaveMessage = 'SMTP saved. Run a test send to verify before the project will use it.';
		} catch (err) {
			smtpSaveError = err instanceof Error ? err.message : 'Failed to save SMTP config';
		} finally {
			smtpSaving = false;
		}
	}

	async function handleDeleteSmtp() {
		if (!confirm('Disconnect custom SMTP? The project will fall back to the platform sender.')) return;
		smtpSaving = true;
		smtpSaveError = '';
		smtpSaveMessage = '';
		try {
			await api.deleteProjectEmailSender(projectCtx.id);
			hydrateSmtp(null);
			smtpSaveMessage = 'Custom SMTP disconnected — using platform sender.';
		} catch (err) {
			smtpSaveError = err instanceof Error ? err.message : 'Failed to disconnect SMTP';
		} finally {
			smtpSaving = false;
		}
	}

	async function handleTestSmtp() {
		smtpTesting = true;
		smtpTestError = '';
		smtpTestMessage = '';
		try {
			await api.testProjectEmailSender(projectCtx.id, smtpTestTo);
			smtpTestMessage = `Test email delivered to ${smtpTestTo}. Sender is now marked verified — auth emails will start routing through it.`;
			// Refresh existing so verified_at + cleared last_error reflect.
			const s = await api.getProjectEmailSender(projectCtx.id);
			smtpExisting = s;
		} catch (err) {
			smtpTestError = err instanceof Error ? err.message : 'Test send failed';
			// Refresh so the new last_error / last_error_at are visible.
			try {
				const s = await api.getProjectEmailSender(projectCtx.id);
				smtpExisting = s;
			} catch { /* ignore secondary fetch error */ }
		} finally {
			smtpTesting = false;
		}
	}

	function hydrateRateLimits(rl: RateLimits | undefined) {
		rlEmailsPerHour = rl?.emails_per_hour ? String(rl.emails_per_hour) : '';
		rlSmsPerHour = rl?.sms_per_hour ? String(rl.sms_per_hour) : '';
		rlTokenRefresh = rl?.token_refresh_per_5min_per_ip ? String(rl.token_refresh_per_5min_per_ip) : '';
		rlTokenVerify = rl?.token_verification_per_5min_per_ip ? String(rl.token_verification_per_5min_per_ip) : '';
		rlSignupSignin = rl?.signup_signin_per_5min_per_ip ? String(rl.signup_signin_per_5min_per_ip) : '';
		// trust_proxy: undefined → platform default; explicit value → use it.
		rlTrustProxy = rl?.trust_proxy ?? DEFAULT_RATE_LIMITS.trust_proxy;
		// Reset on hydrate (initial load + post-save reload). The
		// project's persisted choice is now what the toggle reflects;
		// the next save should NOT pin trust_proxy unless the user
		// clicks again.
		rlTrustProxyTouched = false;
	}

	function parseIntOrUndef(s: string): number | undefined {
		const t = s.trim();
		if (!t) return undefined;
		const n = parseInt(t, 10);
		if (Number.isNaN(n) || n < 0) return undefined;
		return n;
	}

	async function handleSaveRateLimits() {
		rlSaving = true;
		rlSaveMessage = '';
		rlSaveError = '';
		try {
			const rl: RateLimits = {
				signup_signin_per_5min_per_ip: parseIntOrUndef(rlSignupSignin),
				token_refresh_per_5min_per_ip: parseIntOrUndef(rlTokenRefresh),
				token_verification_per_5min_per_ip: parseIntOrUndef(rlTokenVerify),
				emails_per_hour: parseIntOrUndef(rlEmailsPerHour),
				sms_per_hour: parseIntOrUndef(rlSmsPerHour)
			};
			// Only persist trust_proxy when the user explicitly touched
			// the toggle. Otherwise leave it absent so the backend's
			// *bool merge picks up the platform default — including any
			// future #238 default flip. A no-op save must not silently
			// pin an explicit `false`.
			if (rlTrustProxyTouched) {
				rl.trust_proxy = rlTrustProxy;
			}

			// Merge the rate_limits sub-object into the existing
			// auth_config so we don't blow away other fields (providers,
			// CORS, etc). The backend PATCH replaces auth_config wholesale,
			// so we have to send the full struct.
			const existing = projectCtx.project?.auth_config;
			if (!existing) {
				throw new Error('Auth config not loaded yet — wait for the project to finish loading.');
			}
			const merged: AuthConfig = { ...existing, rate_limits: rl };
			const updated = await api.updateProject(projectCtx.id, { auth_config: merged });
			if (updated) {
				projectCtx.updateProject(updated);
				hydrateRateLimits(updated.auth_config?.rate_limits);
			}
			rlSaveMessage = 'Rate limits saved.';
			setTimeout(() => { rlSaveMessage = ''; }, 3000);
		} catch (err) {
			rlSaveError = err instanceof Error ? err.message : 'Failed to save';
		} finally {
			rlSaving = false;
		}
	}

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
			phoneEnabled = cfg.providers?.phone?.enabled ?? false;
			requireEmailConfirmation = cfg.require_email_confirmation;
			passwordMinLength = cfg.password_min_length;
			sessionDuration = cfg.session_duration;
			redirectUrls = cfg.redirect_urls.join('\n');
			corsOrigins = (cfg.cors_origins ?? []).join('\n');
			emailVerificationUrl = projectCtx.project?.auth_config?.email_verification_url ?? '';
			passwordResetUrl = projectCtx.project?.auth_config?.password_reset_url ?? '';
			magicLinkUrl = projectCtx.project?.auth_config?.magic_link_url ?? '';

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
			if (oauthCfg?.microsoft) {
				microsoftEnabled = oauthCfg.microsoft.enabled ?? false;
				microsoftClientId = oauthCfg.microsoft.client_id ?? '';
				microsoftTenantId = oauthCfg.microsoft.tenant_id ?? '';
				microsoftSecretSet = oauthCfg.microsoft.secret_set ?? false;
				microsoftClientSecret = '';
				microsoftSecretDirty = false;
			}

			hydrateRateLimits(projectCtx.project?.auth_config?.rate_limits);
		}
	});

	/** Case-insensitive scheme+host, case-sensitive path/query/fragment
	 * — mirrors the backend's `urlsMatch` in internal/tenant/auth_config.go
	 * so the frontend doesn't reject URLs the backend would accept
	 * (e.g. a redirect list containing `https://App.Example.com/verify`
	 * matched against `https://app.example.com/verify`). Falls back to
	 * case-insensitive plain compare when either side won't parse
	 * (custom-scheme tenants like `myapp://verify`).
	 */
	function urlInAllowlist(candidate: string, list: string[]): boolean {
		const cTrim = candidate.trim();
		for (const raw of list) {
			const listTrim = raw.trim();
			if (cTrim === listTrim) return true;
			try {
				const a = new URL(cTrim);
				const b = new URL(listTrim);
				if (a.protocol.toLowerCase() !== b.protocol.toLowerCase()) continue;
				if (a.host.toLowerCase() !== b.host.toLowerCase()) continue;
				if (a.pathname !== b.pathname) continue;
				if (a.search !== b.search) continue;
				if (a.hash !== b.hash) continue;
				return true;
			} catch {
				// One side isn't a parseable URL — fall back to plain
				// case-insensitive compare so custom-scheme tenants
				// still work.
				if (cTrim.toLowerCase() === listTrim.toLowerCase()) return true;
			}
		}
		return false;
	}

	async function handleSave() {
		saveMessage = '';
		saveError = '';

		// #260 client-side gate — runs BEFORE `saving = true` so a
		// rejection doesn't flash the button spinner (review nit).
		// The backend runs the same check; catching it here spares
		// the round-trip and puts the error next to the offending
		// field.
		const redirectList = redirectUrls.split('\n').map(u => u.trim()).filter(Boolean);
		const inAllowlist = (u: string) => !u || urlInAllowlist(u, redirectList);
		const evuTrim = emailVerificationUrl.trim();
		const pruTrim = passwordResetUrl.trim();
		const mluTrim = magicLinkUrl.trim();
		if (!inAllowlist(evuTrim)) {
			saveError = 'Email verification URL must appear in the redirect URLs list above.';
			return;
		}
		if (!inAllowlist(pruTrim)) {
			saveError = 'Password reset URL must appear in the redirect URLs list above.';
			return;
		}
		if (!inAllowlist(mluTrim)) {
			saveError = 'Magic link URL must appear in the redirect URLs list above.';
			return;
		}
		// Extra guard: if the user turned on email confirmation but
		// hasn't set a verification URL, the backend will 400 on the
		// first signup. Fail loud in the UI so the operator knows.
		if (requireEmailConfirmation && !evuTrim) {
			saveError = 'Email verification is enabled but no verification URL is configured. Set one below or turn off "require email confirmation".';
			return;
		}

		saving = true;
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
			const microsoftProvider: Record<string, any> = {
				enabled: microsoftEnabled,
				client_id: microsoftClientId,
				tenant_id: microsoftTenantId
			};
			if (microsoftSecretDirty && microsoftClientSecret) {
				microsoftProvider.client_secret = microsoftClientSecret;
			}

			const config: AuthConfig = {
				providers: { email_password: { enabled: emailPasswordEnabled }, magic_link: { enabled: magicLinkEnabled }, phone: { enabled: phoneEnabled } },
				oauth_providers: {
					google: googleProvider as any,
					github: githubProvider as any,
					linkedin: linkedinProvider as any,
					apple: appleProvider as any,
					microsoft: microsoftProvider as any
				},
				password_min_length: passwordMinLength,
				require_email_confirmation: requireEmailConfirmation,
				session_duration: sessionDuration,
				redirect_urls: redirectUrls.split('\n').map(u => u.trim()).filter(Boolean),
				cors_origins: corsOrigins.split('\n').map(u => u.trim()).filter(Boolean)
			};
			// #260 email-flow URLs. Trimmed; empty string sent as `undefined`
			// so the backend receives "field absent" and stores NULL — matches
			// the "no default configured" resolver branch on the backend.
			const evu = emailVerificationUrl.trim();
			const pru = passwordResetUrl.trim();
			const mlu = magicLinkUrl.trim();
			if (evu) config.email_verification_url = evu;
			if (pru) config.password_reset_url = pru;
			if (mlu) config.magic_link_url = mlu;
			const updated = await api.updateProject(projectCtx.id, { auth_config: config });

			// Update the project context so navigation within the same project
			// picks up the saved auth_config without a full reload.
			if (updated) {
				projectCtx.updateProject(updated);
			}

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
			if (oauthCfg?.microsoft) {
				microsoftSecretSet = oauthCfg.microsoft.secret_set ?? microsoftSecretSet;
			}
			googleClientSecret = '';
			googleSecretDirty = false;
			githubClientSecret = '';
			githubSecretDirty = false;
			linkedinClientSecret = '';
			linkedinSecretDirty = false;
			applePrivateKey = '';
			appleSecretDirty = false;
			microsoftClientSecret = '';
			microsoftSecretDirty = false;

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
			<button
				onclick={() => activeTab = 'rate_limits'}
				class="pb-3 text-sm font-medium border-b-2 transition-colors cursor-pointer {activeTab === 'rate_limits' ? 'border-eurobase-600 text-eurobase-600' : 'border-transparent text-gray-500 hover:text-gray-700 hover:border-gray-300'}"
			>
				Rate Limits
			</button>
			<button
				onclick={() => { activeTab = 'smtp'; loadSmtp(); }}
				class="pb-3 text-sm font-medium border-b-2 transition-colors cursor-pointer {activeTab === 'smtp' ? 'border-eurobase-600 text-eurobase-600' : 'border-transparent text-gray-500 hover:text-gray-700 hover:border-gray-300'}"
			>
				SMTP
			</button>
		</nav>
	</div>

	{#if saveMessage || templateMessage || rlSaveMessage}
		<div class="mt-4 rounded-lg bg-emerald-50 border border-emerald-200 px-4 py-3 text-sm text-emerald-700">
			{saveMessage || templateMessage || rlSaveMessage}
		</div>
	{/if}

	{#if saveError || templateError || rlSaveError}
		<div class="mt-4 rounded-lg bg-red-50 border border-red-200 px-4 py-3 text-sm text-red-700">
			{saveError || templateError || rlSaveError}
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

					<!-- Phone Auth (SMS OTP) -->
					<div class="rounded-lg border border-gray-200 px-4 py-3">
						<div class="flex items-center justify-between">
							<div>
								<p class="text-sm font-medium text-gray-900">Phone (SMS OTP)</p>
								<p class="text-xs text-gray-500">Sign in with phone number via SMS verification code</p>
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
						{#if phoneEnabled}
							<div class="mt-3 rounded-lg bg-eurobase-50 border border-eurobase-100 p-3 space-y-2">
								<p class="text-xs text-eurobase-700 leading-relaxed">Users enter their phone number and receive a 6-digit code via SMS. The code expires after 10 minutes. Phone-only users are created without an email address.</p>
								<p class="text-xs font-medium text-eurobase-800 mt-2">REST API</p>
								<div class="rounded-md bg-gray-900 p-2.5 font-mono text-[11px] text-green-400 leading-relaxed overflow-x-auto">
									<div><span class="text-amber-400">POST</span> /v1/auth/phone/send-otp</div>
									<div class="text-gray-400">Body: {"{"}"phone": "+33612345678"{"}"}</div>
									<div class="mt-1.5"><span class="text-amber-400">POST</span> /v1/auth/phone/verify</div>
									<div class="text-gray-400">Body: {"{"}"phone": "+33612345678", "code": "123456"{"}"}</div>
								</div>
								<p class="text-[11px] text-eurobase-600 mt-1">Requires GATEWAYAPI_TOKEN environment variable on the gateway. SMS is sent via GatewayAPI (EU-based, Denmark).</p>
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
										<li>Add this as an authorized redirect URI:
											<code class="mt-1 block bg-eurobase-100 px-2 py-1 rounded text-[11px] break-all select-all">{projectCtx.project?.api_url}/v1/auth/oauth/google/callback</code>
										</li>
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
										<li>Set Authorization callback URL to:
											<code class="mt-1 block bg-eurobase-100 px-2 py-1 rounded text-[11px] break-all select-all">{projectCtx.project?.api_url}/v1/auth/oauth/github/callback</code>
										</li>
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
										<li>Under OAuth 2.0 settings, add this as an authorized redirect URL:
											<code class="mt-1 block bg-eurobase-100 px-2 py-1 rounded text-[11px] break-all select-all">{projectCtx.project?.api_url}/v1/auth/oauth/linkedin/callback</code>
										</li>
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
										<li>Enable "Sign In with Apple" and configure the return URL to:
											<code class="mt-1 block bg-eurobase-100 px-2 py-1 rounded text-[11px] break-all select-all">{projectCtx.project?.api_url}/v1/auth/oauth/apple/callback</code>
										</li>
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

					<!-- Microsoft / Office 365 OAuth -->
					<div class="rounded-lg border border-gray-200 px-4 py-3">
						<div class="flex items-center justify-between">
							<div>
								<p class="text-sm font-medium text-gray-900">Microsoft</p>
								<p class="text-xs text-gray-500">Sign in with Microsoft / Office 365 / Entra ID</p>
							</div>
							<button
								type="button"
								role="switch"
								aria-checked={microsoftEnabled}
								onclick={() => microsoftEnabled = !microsoftEnabled}
								class="relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 {microsoftEnabled ? 'bg-eurobase-600' : 'bg-gray-200'}"
							>
								<span class="pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out {microsoftEnabled ? 'translate-x-5' : 'translate-x-0'}"></span>
							</button>
						</div>
						{#if microsoftEnabled}
							<div class="mt-3 space-y-3">
								<div>
									<label for="microsoft-client-id" class="block text-xs font-medium text-gray-700">Application (client) ID</label>
									<input id="microsoft-client-id" type="text" bind:value={microsoftClientId} placeholder="00000000-0000-0000-0000-000000000000" class="mt-1 block w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors" />
								</div>
								<div>
									<label for="microsoft-tenant-id" class="block text-xs font-medium text-gray-700">
										Tenant ID
										<span class="ml-1 text-gray-400 font-normal">(leave blank for multi-tenant + personal accounts)</span>
									</label>
									<input id="microsoft-tenant-id" type="text" bind:value={microsoftTenantId} placeholder="common  |  organizations  |  consumers  |  &lt;tenant-guid&gt;" class="mt-1 block w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors font-mono" />
									<p class="mt-1 text-xs text-gray-500">Use a specific tenant GUID to restrict sign-in to a single organisation (enterprise SSO).</p>
								</div>
								<div>
									<label for="microsoft-client-secret" class="block text-xs font-medium text-gray-700">
										Client secret
										{#if microsoftSecretSet && !microsoftSecretDirty}
											<span class="ml-2 inline-flex items-center gap-1 rounded-full bg-green-50 px-2 py-0.5 text-[10px] font-medium text-green-700 border border-green-200">
												<svg class="h-3 w-3" fill="none" viewBox="0 0 24 24" stroke-width="2.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
												Encrypted in vault
											</span>
										{/if}
									</label>
									<input
										id="microsoft-client-secret"
										type="password"
										bind:value={microsoftClientSecret}
										oninput={() => microsoftSecretDirty = true}
										placeholder={microsoftSecretSet ? '•••••••••••••• (leave blank to keep current)' : 'Client secret value (not the secret ID)'}
										class="mt-1 block w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
									/>
								</div>
								<div class="rounded-lg bg-eurobase-50 border border-eurobase-100 p-3">
									<p class="text-xs font-medium text-eurobase-800">Setup instructions</p>
									<ol class="mt-1 text-xs text-eurobase-700 list-decimal list-inside space-y-1">
										<li>Go to <a href="https://portal.azure.com/#view/Microsoft_AAD_RegisteredApps/ApplicationsListBlade" target="_blank" rel="noopener" class="underline">Azure Portal → App registrations</a> and create a new registration</li>
										<li>Set the redirect URI (type: Web): your Eurobase API URL + <code class="bg-eurobase-100 px-1 rounded">/v1/auth/oauth/microsoft/callback</code></li>
										<li>Copy the <strong>Application (client) ID</strong> into the field above</li>
										<li>Copy the <strong>Directory (tenant) ID</strong> if restricting to one organisation, otherwise leave blank</li>
										<li>Under <em>Certificates &amp; secrets</em> → <em>New client secret</em>, copy the <strong>Value</strong> (not the Secret ID) into the field above</li>
										<li>Under <em>API permissions</em>, ensure the delegated permissions <code class="bg-eurobase-100 px-1 rounded">openid</code>, <code class="bg-eurobase-100 px-1 rounded">email</code>, <code class="bg-eurobase-100 px-1 rounded">profile</code>, <code class="bg-eurobase-100 px-1 rounded">offline_access</code> are granted</li>
									</ol>
								</div>
								<div class="rounded-lg bg-amber-50 border border-amber-200 p-3">
									<p class="text-xs text-amber-700">Sign-in redirects transit Microsoft infrastructure. User records and application data remain in Eurobase (Scaleway fr-par). For strict EU-only authentication, restrict to a single Entra tenant hosted in the EU geo.</p>
								</div>
							</div>
						{/if}
					</div>

					{#if googleEnabled || githubEnabled || linkedinEnabled || appleEnabled || microsoftEnabled}
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

					<div>
						<label for="cors-origins" class="block text-sm font-medium text-gray-700">Allowed CORS origins</label>
						<p class="text-xs text-gray-400 mt-0.5">Browser origins permitted to call this project's API. One per line, in the form <code class="bg-gray-100 border border-gray-200 rounded px-1">scheme://host[:port]</code> with no path or trailing slash (e.g. <code class="bg-gray-100 border border-gray-200 rounded px-1">http://localhost:3000</code>, <code class="bg-gray-100 border border-gray-200 rounded px-1">https://app.example.com</code>). Eurobase platform origins (<code class="bg-gray-100 border border-gray-200 rounded px-1">*.eurobase.app</code>) are always allowed — this list is additive for your own apps. Leave empty if you only call the API from a server.</p>
						<textarea
							id="cors-origins"
							bind:value={corsOrigins}
							rows="3"
							placeholder="http://localhost:3000&#10;https://app.example.com"
							class="mt-1.5 block w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm font-mono focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
						></textarea>
					</div>

					<!-- Email-flow redirect URLs (#260, part of #257) -->
					<div class="mt-4 rounded-lg border border-gray-200 bg-gray-50 p-4">
						<h3 class="text-sm font-semibold text-gray-900">Email-flow redirect URLs</h3>
						<p class="mt-1 text-xs text-gray-500">
							Where the verification / password-reset / magic-link emails send your users. Each URL must appear in the <strong>Allowed redirect URLs</strong> list above (same allowlist). Your app reads the <code class="bg-white border border-gray-200 rounded px-1">?token=...</code> query parameter and calls the matching SDK method (<code class="bg-white border border-gray-200 rounded px-1">eb.auth.verifyEmail</code>, <code class="bg-white border border-gray-200 rounded px-1">eb.auth.resetPassword</code>, <code class="bg-white border border-gray-200 rounded px-1">eb.auth.signInWithMagicLink</code>). Leave blank if you're not using that flow.
						</p>

						<div class="mt-3 space-y-3">
							<div>
								<label for="verify-url" class="block text-xs font-medium text-gray-700">Email verification URL {#if requireEmailConfirmation}<span class="text-red-600">*</span>{/if}</label>
								<input
									id="verify-url"
									type="url"
									bind:value={emailVerificationUrl}
									placeholder="https://yourapp.example/verify"
									class="mt-1 block w-full rounded-lg border border-gray-300 px-3 py-1.5 text-sm text-gray-900 shadow-sm font-mono focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
								/>
								{#if requireEmailConfirmation && !emailVerificationUrl.trim()}
									<p class="mt-1 text-xs text-red-600">Required — email confirmation is enabled above.</p>
								{/if}
							</div>

							<div>
								<label for="reset-url" class="block text-xs font-medium text-gray-700">Password reset URL</label>
								<input
									id="reset-url"
									type="url"
									bind:value={passwordResetUrl}
									placeholder="https://yourapp.example/reset-password"
									class="mt-1 block w-full rounded-lg border border-gray-300 px-3 py-1.5 text-sm text-gray-900 shadow-sm font-mono focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
								/>
							</div>

							<div>
								<label for="magic-url" class="block text-xs font-medium text-gray-700">Magic link URL {#if magicLinkEnabled}<span class="text-amber-600">*</span>{/if}</label>
								<input
									id="magic-url"
									type="url"
									bind:value={magicLinkUrl}
									placeholder="https://yourapp.example/magic-link"
									class="mt-1 block w-full rounded-lg border border-gray-300 px-3 py-1.5 text-sm text-gray-900 shadow-sm font-mono focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
								/>
								{#if magicLinkEnabled && !magicLinkUrl.trim()}
									<p class="mt-1 text-xs text-amber-600">Recommended — magic link auth is enabled but requests will silently no-op until this is set.</p>
								{/if}
							</div>
						</div>
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

	<!-- Rate Limits Tab (#229 / umbrella #224) -->
	{#if activeTab === 'rate_limits'}
		<div class="mt-6 space-y-6 max-w-2xl">
			<div>
				<h3 class="text-sm font-semibold text-gray-900">Rate Limits</h3>
				<p class="mt-1 text-xs text-gray-500">
					Safeguard against bursts of incoming traffic to prevent abuse and protect your project's email/SMS budget. Empty fields use the platform default shown in the placeholder.
				</p>
			</div>

			<!-- Rate limit fields, modeled on the Supabase Rate Limits page -->
			<div class="rounded-lg border border-gray-200 bg-white divide-y divide-gray-200">

				<!-- Emails / hour -->
				<div class="flex items-start gap-4 p-4">
					<div class="flex-1">
						<label for="rl-emails" class="text-sm font-medium text-gray-900">Rate limit for sending emails</label>
						<p class="mt-0.5 text-xs text-gray-500">
							Number of emails (verification, password reset, magic link) that can be sent per hour from your project.
							<span class="block mt-0.5 text-amber-700">Note: enforcement is parked behind the BYO-SMTP feature (#235); the field saves but isn't applied yet.</span>
						</p>
					</div>
					<div class="flex items-center gap-2 shrink-0">
						<input
							id="rl-emails"
							type="number"
							min="0"
							bind:value={rlEmailsPerHour}
							placeholder={String(DEFAULT_RATE_LIMITS.emails_per_hour)}
							class="w-24 rounded-md border border-gray-300 px-3 py-1.5 text-sm focus:border-eurobase-600 focus:outline-none focus:ring-1 focus:ring-eurobase-600"
						/>
						<span class="text-xs text-gray-500">emails/h</span>
					</div>
				</div>

				<!-- SMS / hour -->
				<div class="flex items-start gap-4 p-4">
					<div class="flex-1">
						<label for="rl-sms" class="text-sm font-medium text-gray-900">Rate limit for sending SMS messages</label>
						<p class="mt-0.5 text-xs text-gray-500">
							Number of SMS one-time codes that can be sent per hour from your project. Over-quota sends are silently skipped server-side; the operator log shows the cap-hit.
						</p>
					</div>
					<div class="flex items-center gap-2 shrink-0">
						<input
							id="rl-sms"
							type="number"
							min="0"
							bind:value={rlSmsPerHour}
							placeholder={String(DEFAULT_RATE_LIMITS.sms_per_hour)}
							class="w-24 rounded-md border border-gray-300 px-3 py-1.5 text-sm focus:border-eurobase-600 focus:outline-none focus:ring-1 focus:ring-eurobase-600"
						/>
						<span class="text-xs text-gray-500">sms/h</span>
					</div>
				</div>

				<!-- Token refresh / 5 min / IP -->
				<div class="flex items-start gap-4 p-4">
					<div class="flex-1">
						<label for="rl-refresh" class="text-sm font-medium text-gray-900">Rate limit for token refreshes</label>
						<p class="mt-0.5 text-xs text-gray-500">
							Number of <code class="text-[11px] bg-gray-100 px-1 rounded">/v1/auth/refresh</code> calls allowed in a 5-minute interval per IP address. Higher because legitimate SDK clients refresh proactively.
						</p>
						{#if rlTokenRefresh}
							<p class="mt-0.5 text-[11px] text-gray-400">≈ {parseInt(rlTokenRefresh, 10) * 12} requests/hour</p>
						{/if}
					</div>
					<div class="flex items-center gap-2 shrink-0">
						<input
							id="rl-refresh"
							type="number"
							min="0"
							bind:value={rlTokenRefresh}
							placeholder={String(DEFAULT_RATE_LIMITS.token_refresh_per_5min_per_ip)}
							class="w-24 rounded-md border border-gray-300 px-3 py-1.5 text-sm focus:border-eurobase-600 focus:outline-none focus:ring-1 focus:ring-eurobase-600"
						/>
						<span class="text-xs text-gray-500">/5 min</span>
					</div>
				</div>

				<!-- Token verification / 5 min / IP -->
				<div class="flex items-start gap-4 p-4">
					<div class="flex-1">
						<label for="rl-verify" class="text-sm font-medium text-gray-900">Rate limit for token verifications</label>
						<p class="mt-0.5 text-xs text-gray-500">
							Number of OTP, magic-link, and email-verify attempts allowed in a 5-minute interval per IP. Throttles brute-force against 6-digit phone OTPs.
						</p>
						{#if rlTokenVerify}
							<p class="mt-0.5 text-[11px] text-gray-400">≈ {parseInt(rlTokenVerify, 10) * 12} requests/hour</p>
						{/if}
					</div>
					<div class="flex items-center gap-2 shrink-0">
						<input
							id="rl-verify"
							type="number"
							min="0"
							bind:value={rlTokenVerify}
							placeholder={String(DEFAULT_RATE_LIMITS.token_verification_per_5min_per_ip)}
							class="w-24 rounded-md border border-gray-300 px-3 py-1.5 text-sm focus:border-eurobase-600 focus:outline-none focus:ring-1 focus:ring-eurobase-600"
						/>
						<span class="text-xs text-gray-500">/5 min</span>
					</div>
				</div>

				<!-- Sign-up + sign-in / 5 min / IP -->
				<div class="flex items-start gap-4 p-4">
					<div class="flex-1">
						<label for="rl-signup" class="text-sm font-medium text-gray-900">Rate limit for sign-ups and sign-ins</label>
						<p class="mt-0.5 text-xs text-gray-500">
							Combined volume cap on signup and signin requests per IP, per 5 minutes. The per-account brute-force counter (signin failures by email) is a separate axis at platform defaults.
						</p>
						{#if rlSignupSignin}
							<p class="mt-0.5 text-[11px] text-gray-400">≈ {parseInt(rlSignupSignin, 10) * 12} requests/hour</p>
						{/if}
					</div>
					<div class="flex items-center gap-2 shrink-0">
						<input
							id="rl-signup"
							type="number"
							min="0"
							bind:value={rlSignupSignin}
							placeholder={String(DEFAULT_RATE_LIMITS.signup_signin_per_5min_per_ip)}
							class="w-24 rounded-md border border-gray-300 px-3 py-1.5 text-sm focus:border-eurobase-600 focus:outline-none focus:ring-1 focus:ring-eurobase-600"
						/>
						<span class="text-xs text-gray-500">/5 min</span>
					</div>
				</div>

			</div>

			<!-- IP Address Forwarding -->
			<div>
				<h3 class="text-sm font-semibold text-gray-900">IP Address Forwarding</h3>
				<p class="mt-1 text-xs text-gray-500">Control how the rate limiter determines the source IP address.</p>
			</div>

			<div class="rounded-lg border border-gray-200 bg-white p-4">
				<div class="flex items-start gap-4">
					<div class="flex-1">
						<label for="rl-trust-proxy" class="text-sm font-medium text-gray-900">Trust X-Forwarded-For</label>
						<p class="mt-0.5 text-xs text-gray-500">
							When <strong>on</strong>, the limiter keys on the leftmost <code class="text-[11px] bg-gray-100 px-1 rounded">X-Forwarded-For</code> entry (the real client IP). Only safe when exactly one trusted hop in front of the gateway authoritatively overwrites that header.
						</p>
						<p class="mt-0.5 text-xs text-gray-500">
							When <strong>off</strong> (default), the limiter keys on the TCP peer — safe under any header forgery, but in deployments behind one shared ingress the counter collapses to a per-project total.
						</p>
						<p class="mt-1 text-[11px] text-amber-700">
							Eurobase ships <strong>off</strong> by default until the Scaleway LB / nginx-ingress XFF chain is verified end-to-end (#238).
						</p>
					</div>
					<div class="shrink-0 pt-1">
						<button
							id="rl-trust-proxy"
							onclick={() => { rlTrustProxy = !rlTrustProxy; rlTrustProxyTouched = true; }}
							role="switch"
							aria-checked={rlTrustProxy}
							class="relative inline-flex h-6 w-11 items-center rounded-full transition-colors {rlTrustProxy ? 'bg-eurobase-600' : 'bg-gray-300'}"
						>
							<span class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform {rlTrustProxy ? 'translate-x-6' : 'translate-x-1'}"></span>
						</button>
					</div>
				</div>
			</div>

			<div class="flex justify-end">
				<button
					type="button"
					onclick={handleSaveRateLimits}
					disabled={rlSaving}
					class="rounded-md bg-eurobase-600 px-4 py-2 text-sm font-medium text-white shadow-sm hover:bg-eurobase-700 disabled:opacity-60 disabled:cursor-not-allowed transition-colors"
				>
					{rlSaving ? 'Saving…' : 'Save changes'}
				</button>
			</div>
		</div>
	{/if}

	<!-- SMTP Tab (#235 Part 1, BYO custom SMTP) -->
	{#if activeTab === 'smtp'}
		<div class="mt-6 space-y-6 max-w-2xl">
			<div>
				<h3 class="text-sm font-semibold text-gray-900">Custom SMTP sender</h3>
				<p class="mt-1 text-xs text-gray-500">
					Bring your own SMTP provider for auth emails (verification, password reset, magic link). When configured + verified, this project's emails route through your provider instead of the shared Eurobase sender — useful for higher deliverability and owning your sender reputation. Leave blank to keep using the platform sender.
				</p>
			</div>

			{#if smtpLoading}
				<div class="text-sm text-gray-500">Loading…</div>
			{:else}
				{#if smtpExisting}
					<div class="rounded-lg border border-gray-200 bg-gray-50 p-4 text-xs space-y-1.5">
						<div class="flex items-center gap-2">
							{#if smtpExisting.verified_at}
								<span class="inline-flex items-center gap-1.5 rounded-full bg-emerald-100 text-emerald-700 px-2 py-0.5 font-medium">
									<span class="w-1.5 h-1.5 rounded-full bg-emerald-500"></span>
									Verified
								</span>
								<span class="text-gray-500">Last verified {new Date(smtpExisting.verified_at).toLocaleString()}</span>
							{:else}
								<span class="inline-flex items-center gap-1.5 rounded-full bg-amber-100 text-amber-800 px-2 py-0.5 font-medium">
									<span class="w-1.5 h-1.5 rounded-full bg-amber-500"></span>
									Not verified
								</span>
								<span class="text-gray-500">Run a test send below — the project keeps using the platform sender until then.</span>
							{/if}
						</div>
						{#if smtpExisting.last_error}
							<div class="text-red-700 mt-1">
								<span class="font-medium">Last error:</span> {smtpExisting.last_error}
								{#if smtpExisting.last_error_at}
									<span class="text-gray-500">· {new Date(smtpExisting.last_error_at).toLocaleString()}</span>
								{/if}
							</div>
						{/if}
						{#if smtpExisting.sovereignty_warning}
							<div class="text-amber-800 mt-1">⚠ {smtpExisting.sovereignty_warning}</div>
						{/if}
					</div>
				{/if}

				<div class="rounded-lg border border-gray-200 bg-white divide-y divide-gray-200">
					<div class="p-4 space-y-3">
						<div class="grid grid-cols-3 gap-3">
							<div class="col-span-2">
								<label for="smtp-host" class="block text-xs font-medium text-gray-700 mb-1">Host</label>
								<input id="smtp-host" type="text" bind:value={smtpHost} placeholder="smtp.example.com"
									class="w-full rounded-md border border-gray-300 px-3 py-1.5 text-sm focus:border-eurobase-600 focus:outline-none focus:ring-1 focus:ring-eurobase-600" />
							</div>
							<div>
								<label for="smtp-port" class="block text-xs font-medium text-gray-700 mb-1">Port</label>
								<input id="smtp-port" type="number" min="1" max="65535" bind:value={smtpPort} placeholder="587"
									class="w-full rounded-md border border-gray-300 px-3 py-1.5 text-sm focus:border-eurobase-600 focus:outline-none focus:ring-1 focus:ring-eurobase-600" />
							</div>
						</div>
						<div>
							<label for="smtp-encryption" class="block text-xs font-medium text-gray-700 mb-1">Encryption</label>
							<select id="smtp-encryption" bind:value={smtpEncryption}
								class="w-full rounded-md border border-gray-300 px-3 py-1.5 text-sm focus:border-eurobase-600 focus:outline-none focus:ring-1 focus:ring-eurobase-600">
								<option value="starttls">STARTTLS (port 587, recommended)</option>
								<option value="tls">TLS / SMTPS (port 465)</option>
								<option value="none">None — plaintext (do not use over the internet)</option>
							</select>
						</div>
					</div>

					<div class="p-4 space-y-3">
						<div>
							<label for="smtp-username" class="block text-xs font-medium text-gray-700 mb-1">Username</label>
							<input id="smtp-username" type="text" bind:value={smtpUsername} placeholder="apikey or you@example.com"
								class="w-full rounded-md border border-gray-300 px-3 py-1.5 text-sm focus:border-eurobase-600 focus:outline-none focus:ring-1 focus:ring-eurobase-600" />
						</div>
						<div>
							<label for="smtp-password" class="block text-xs font-medium text-gray-700 mb-1">
								Password
								{#if smtpExisting?.has_password}
									<span class="text-gray-400 font-normal">(leave blank to keep the saved one)</span>
								{/if}
							</label>
							<input id="smtp-password" type="password" bind:value={smtpPassword} placeholder={smtpExisting?.has_password ? '••••••••' : 'SMTP password'}
								class="w-full rounded-md border border-gray-300 px-3 py-1.5 text-sm focus:border-eurobase-600 focus:outline-none focus:ring-1 focus:ring-eurobase-600" />
							<p class="mt-1 text-[11px] text-gray-500">Stored encrypted at rest with your project's per-tenant key. Never returned by the API after save.</p>
						</div>
					</div>

					<div class="p-4 space-y-3">
						<div>
							<label for="smtp-from-email" class="block text-xs font-medium text-gray-700 mb-1">From address</label>
							<input id="smtp-from-email" type="email" bind:value={smtpFromEmail} placeholder="noreply@yourdomain.com"
								class="w-full rounded-md border border-gray-300 px-3 py-1.5 text-sm focus:border-eurobase-600 focus:outline-none focus:ring-1 focus:ring-eurobase-600" />
						</div>
						<div>
							<label for="smtp-from-name" class="block text-xs font-medium text-gray-700 mb-1">From name <span class="text-gray-400 font-normal">(optional)</span></label>
							<input id="smtp-from-name" type="text" bind:value={smtpFromName} placeholder="Your Product"
								class="w-full rounded-md border border-gray-300 px-3 py-1.5 text-sm focus:border-eurobase-600 focus:outline-none focus:ring-1 focus:ring-eurobase-600" />
						</div>
					</div>
				</div>

				<div class="flex items-center justify-between gap-3">
					{#if smtpExisting}
						<button type="button" onclick={handleDeleteSmtp} disabled={smtpSaving}
							class="rounded-md px-3 py-1.5 text-sm text-red-600 hover:bg-red-50 disabled:opacity-50 transition-colors">
							Disconnect
						</button>
					{:else}
						<div></div>
					{/if}
					<button type="button" onclick={handleSaveSmtp} disabled={smtpSaving}
						class="rounded-md bg-eurobase-600 px-4 py-2 text-sm font-medium text-white shadow-sm hover:bg-eurobase-700 disabled:opacity-60 disabled:cursor-not-allowed transition-colors">
						{smtpSaving ? 'Saving…' : 'Save'}
					</button>
				</div>

				<!-- Test send -->
				{#if smtpExisting}
					<div class="rounded-lg border border-gray-200 bg-white p-4 space-y-3">
						<div>
							<h4 class="text-sm font-semibold text-gray-900">Send test</h4>
							<p class="mt-1 text-xs text-gray-500">A successful test marks the sender verified. Until then auth emails fall back to the platform sender.</p>
						</div>
						<div class="flex gap-2">
							<input type="email" bind:value={smtpTestTo} placeholder="you@example.com"
								class="flex-1 rounded-md border border-gray-300 px-3 py-1.5 text-sm focus:border-eurobase-600 focus:outline-none focus:ring-1 focus:ring-eurobase-600" />
							<button type="button" onclick={handleTestSmtp} disabled={smtpTesting || !smtpTestTo}
								class="rounded-md border border-gray-300 px-4 py-1.5 text-sm font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-50 transition-colors">
								{smtpTesting ? 'Sending…' : 'Send test'}
							</button>
						</div>
						{#if smtpTestMessage}
							<div class="rounded-md bg-emerald-50 border border-emerald-200 px-3 py-2 text-xs text-emerald-700">{smtpTestMessage}</div>
						{/if}
						{#if smtpTestError}
							<div class="rounded-md bg-red-50 border border-red-200 px-3 py-2 text-xs text-red-700">{smtpTestError}</div>
						{/if}
					</div>
				{/if}

				{#if smtpSaveMessage}
					<div class="rounded-md bg-emerald-50 border border-emerald-200 px-3 py-2 text-sm text-emerald-700">{smtpSaveMessage}</div>
				{/if}
				{#if smtpSaveError}
					<div class="rounded-md bg-red-50 border border-red-200 px-3 py-2 text-sm text-red-700">{smtpSaveError}</div>
				{/if}
			{/if}
		</div>
	{/if}
</div>
