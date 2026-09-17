<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { api, APIError, type OrgDetail } from '$lib/api.js';

	let orgId = $derived($page.params.id as string);

	let detail = $state<OrgDetail | null>(null);
	let loading = $state(true);
	let error = $state('');

	// Invite modal state
	let showInvite = $state(false);
	let inviteEmail = $state('');
	let inviteRole = $state<'admin' | 'member'>('member');
	let inviting = $state(false);
	let inviteError = $state('');

	// SSO config panel state (populated on load from detail.sso).
	// The `client_secret_set` flag lets us render "•••••••• (set)"
	// vs "not configured" without ever leaking the plaintext value.
	let ssoProvider = $state('google');
	let ssoIssuer = $state('');
	let ssoClientID = $state('');
	let ssoClientSecret = $state(''); // always-blank on load; user re-enters to update
	let ssoRedirectURL = $state('');
	let ssoSaving = $state(false);
	let ssoError = $state('');
	let ssoSuccess = $state('');

	// SSO enforcement toggle state. `ssoRequired` mirrors
	// organizations.sso_required from the server; the toggle handler
	// fires the PATCH and reloads on success. Failure (e.g. no OIDC
	// configured) shows an inline error under the toggle without
	// flipping the local checkbox.
	let ssoRequired = $state(false);
	let ssoRequiredSaving = $state(false);
	let ssoRequiredError = $state('');

	// Remove-member state — the confirm dialog is in-app (mirrors the
	// invite modal styling below) so the browser's native confirm()
	// doesn't hijack the console UX. `pendingRemove` carries the row
	// so the modal can render the member's email; `removeError`
	// surfaces backend failures (e.g. sole-admin refusal) inside the
	// modal instead of a native alert().
	let removingId = $state('');
	let pendingRemove = $state<{ id: string; email: string } | null>(null);
	let removeError = $state('');
	// Caller's own platform_user id — used to hide the Remove button
	// on the caller's own member row. Self-removal is a distinct flow
	// ("Leave organization") that we haven't shipped yet; the backend
	// also refuses removing the sole admin, so letting the button
	// render for self used to surface a browser alert("Cannot remove
	// the last admin from an organization") after a wasted round-trip.
	let callerUserID = $state<string>('');

	onMount(async () => {
		// Load profile in parallel with org detail — profile gives us
		// the caller's user id so we can hide the Remove-self button
		// on the caller's row. If getProfile fails we still show the
		// page, just with the button visible; backend still refuses
		// the destructive action.
		await Promise.all([
			load(),
			api.getProfile().then((p) => { callerUserID = p.id; }).catch(() => {}),
		]);
	});

	async function load() {
		loading = true;
		error = '';
		try {
			detail = await api.getOrg(orgId);
			if (detail.sso) {
				ssoProvider = detail.sso.provider || 'google';
				ssoIssuer = detail.sso.issuer;
				ssoClientID = detail.sso.client_id;
				ssoRedirectURL = detail.sso.redirect_url;
			}
			ssoRequired = !!detail.org.sso_required;
		} catch (err) {
			if (err instanceof APIError && err.status === 404) {
				error = 'Organization not found or you are not a member.';
			} else {
				error = err instanceof Error ? err.message : 'Failed to load organization';
			}
		} finally {
			loading = false;
		}
	}

	async function handleInvite(e: Event) {
		e.preventDefault();
		if (!inviteEmail.trim()) return;
		inviting = true;
		inviteError = '';
		try {
			await api.inviteOrgMember(orgId, inviteEmail.trim(), inviteRole);
			showInvite = false;
			inviteEmail = '';
			await load();
		} catch (err) {
			inviteError = err instanceof Error ? err.message : 'Failed to invite member';
		} finally {
			inviting = false;
		}
	}

	function openRemove(userId: string, email: string) {
		pendingRemove = { id: userId, email };
		removeError = '';
	}

	function cancelRemove() {
		if (removingId) return; // don't dismiss mid-flight
		pendingRemove = null;
		removeError = '';
	}

	async function confirmRemove() {
		if (!pendingRemove) return;
		const target = pendingRemove;
		removingId = target.id;
		removeError = '';
		try {
			await api.removeOrgMember(orgId, target.id);
			pendingRemove = null;
			await load();
		} catch (err) {
			removeError = err instanceof Error ? err.message : 'Failed to remove member';
		} finally {
			removingId = '';
		}
	}

	async function handleSaveSSO(e: Event) {
		e.preventDefault();
		if (!ssoIssuer.trim() || !ssoClientID.trim() || !ssoClientSecret.trim() || !ssoRedirectURL.trim()) {
			ssoError = 'All fields are required.';
			return;
		}
		ssoSaving = true;
		ssoError = '';
		ssoSuccess = '';
		try {
			await api.updateOrgSSO(orgId, {
				provider: ssoProvider,
				issuer: ssoIssuer.trim(),
				client_id: ssoClientID.trim(),
				client_secret: ssoClientSecret,
				redirect_url: ssoRedirectURL.trim(),
			});
			ssoClientSecret = '';
			ssoSuccess = 'SSO configuration saved.';
			await load();
		} catch (err) {
			ssoError = err instanceof Error ? err.message : 'Failed to save SSO configuration';
		} finally {
			ssoSaving = false;
		}
	}

	async function handleSSORequiredToggle() {
		// Optimistic-toggled `ssoRequired` is what the user just
		// clicked; the local value has already flipped via bind:checked.
		// Snapshot before the request so we can revert on failure
		// (else the checkbox stays in the "requested" state despite
		// the server refusing).
		const requested = ssoRequired;
		ssoRequiredSaving = true;
		ssoRequiredError = '';
		try {
			await api.setOrgSSORequired(orgId, requested);
			await load();
		} catch (err) {
			ssoRequired = !requested; // revert local UI
			ssoRequiredError = err instanceof Error ? err.message : 'Failed to update SSO enforcement';
		} finally {
			ssoRequiredSaving = false;
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
	<title>{detail?.org.name ?? 'Organization'} - Eurobase Console</title>
</svelte:head>

<div class="mx-auto max-w-4xl">
	<div class="mb-6 flex items-center gap-2 text-sm text-gray-500">
		<a href="/organizations" class="hover:text-gray-700">Organizations</a>
		<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
			<path stroke-linecap="round" stroke-linejoin="round" d="m8.25 4.5 7.5 7.5-7.5 7.5" />
		</svg>
		<span class="text-gray-900">{detail?.org.name ?? '…'}</span>
	</div>

	{#if loading}
		<div class="mt-16 flex flex-col items-center text-center">
			<svg class="h-10 w-10 animate-spin text-eurobase-600" fill="none" viewBox="0 0 24 24">
				<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
				<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
			</svg>
		</div>
	{:else if error}
		<div class="mt-4 rounded-lg bg-red-50 border border-red-200 p-4 text-sm text-red-700">
			{error}
			<div class="mt-3">
				<button onclick={() => goto('/organizations')} class="text-sm font-medium text-red-800 hover:text-red-900 underline">Back to organizations</button>
			</div>
		</div>
	{:else if detail}
		<!-- Header -->
		<div class="flex items-start justify-between">
			<div>
				<h1 class="text-2xl font-bold text-gray-900">{detail.org.name}</h1>
				<p class="mt-1 text-sm text-gray-500">
					You are <span class="font-medium">{detail.role}</span> · created {formatDate(detail.org.created_at)}
				</p>
			</div>
		</div>

		<!-- Members section -->
		<section class="mt-8 rounded-xl border border-gray-200 bg-white p-6 shadow-sm">
			<div class="flex items-center justify-between">
				<div>
					<h2 class="text-lg font-semibold text-gray-900">Members</h2>
					<p class="text-sm text-gray-500">{detail.members.length} member{detail.members.length === 1 ? '' : 's'}</p>
				</div>
				{#if detail.role === 'admin'}
					<button
						type="button"
						onclick={() => { showInvite = true; inviteError = ''; inviteEmail = ''; }}
						class="inline-flex items-center gap-2 rounded-lg bg-eurobase-600 px-3 py-2 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 transition-colors cursor-pointer"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
						</svg>
						Invite Member
					</button>
				{/if}
			</div>

			<ul class="mt-4 divide-y divide-gray-100">
				{#each detail.members as m}
					<li class="flex items-center justify-between py-3">
						<div>
							<p class="text-sm font-medium text-gray-900">{m.email}</p>
							<p class="text-xs text-gray-500">
								Joined {formatDate(m.created_at)} · invited via {m.invited_via}
							</p>
						</div>
						<div class="flex items-center gap-3">
							<span class="inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset {m.role === 'admin' ? 'bg-eurobase-50 text-eurobase-700 ring-eurobase-600/20' : 'bg-gray-50 text-gray-600 ring-gray-500/10'}">
								{m.role}
							</span>
							{#if detail.role === 'admin' && m.platform_user_id !== callerUserID}
								<button
									type="button"
									onclick={() => openRemove(m.platform_user_id, m.email)}
									disabled={removingId === m.platform_user_id}
									class="text-xs font-medium text-red-600 hover:text-red-700 disabled:opacity-50 cursor-pointer"
								>
									{removingId === m.platform_user_id ? 'Removing…' : 'Remove'}
								</button>
							{:else if detail.role === 'admin' && m.platform_user_id === callerUserID}
								<span class="text-xs text-gray-400" title="Self-removal is not available here; a Leave organization flow will land in a follow-up">
									(you)
								</span>
							{/if}
						</div>
					</li>
				{/each}
			</ul>
		</section>

		<!-- SSO section (admin-only, per plan) -->
		{#if detail.role === 'admin'}
			<section class="mt-8 rounded-xl border border-gray-200 bg-white p-6 shadow-sm">
				<div>
					<h2 class="text-lg font-semibold text-gray-900">Single Sign-On (OIDC)</h2>
					<p class="text-sm text-gray-500">
						Configure OpenID Connect so invited members can sign in with their work IdP.
						{#if detail.sso}
							<span class="ml-1 inline-flex items-center rounded-full bg-emerald-50 px-2 py-0.5 text-[11px] font-medium text-emerald-700 ring-1 ring-inset ring-emerald-600/20">
								Configured
							</span>
						{/if}
					</p>
				</div>

				{#if ssoError}
					<div class="mt-4 rounded-lg bg-red-50 border border-red-200 p-3 text-sm text-red-700">{ssoError}</div>
				{/if}
				{#if ssoSuccess}
					<div class="mt-4 rounded-lg bg-emerald-50 border border-emerald-200 p-3 text-sm text-emerald-700">{ssoSuccess}</div>
				{/if}

				<form onsubmit={handleSaveSSO} oninput={() => { ssoSuccess = ''; ssoError = ''; }} class="mt-6 space-y-4">
					<div>
						<label for="sso-provider" class="block text-sm font-medium text-gray-700">Provider</label>
						<select
							id="sso-provider"
							bind:value={ssoProvider}
							class="mt-1 block w-full rounded-lg border border-gray-300 px-3.5 py-2.5 text-sm text-gray-900 shadow-sm focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
						>
							<option value="google">Google Workspace</option>
							<option value="microsoft">Microsoft Entra ID</option>
							<option value="okta">Okta</option>
							<option value="authentik">Authentik</option>
							<option value="other">Other OIDC provider</option>
						</select>
					</div>

					<div>
						<label for="sso-issuer" class="block text-sm font-medium text-gray-700">Issuer URL</label>
						<input
							id="sso-issuer"
							type="url"
							bind:value={ssoIssuer}
							placeholder="https://accounts.google.com"
							required
							class="mt-1 block w-full rounded-lg border border-gray-300 px-3.5 py-2.5 text-sm text-gray-900 shadow-sm placeholder:text-gray-400 focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors font-mono"
						/>
						<p class="mt-1 text-xs text-gray-400">The base issuer — Eurobase fetches <code class="rounded bg-gray-100 px-1 py-0.5">/.well-known/openid-configuration</code> from here.</p>
					</div>

					<div>
						<label for="sso-client-id" class="block text-sm font-medium text-gray-700">Client ID</label>
						<input
							id="sso-client-id"
							type="text"
							bind:value={ssoClientID}
							required
							class="mt-1 block w-full rounded-lg border border-gray-300 px-3.5 py-2.5 text-sm text-gray-900 shadow-sm focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors font-mono"
						/>
					</div>

					<div>
						<label for="sso-client-secret" class="block text-sm font-medium text-gray-700">Client Secret</label>
						<input
							id="sso-client-secret"
							type="password"
							bind:value={ssoClientSecret}
							placeholder={detail.sso?.client_secret_set ? '•••••••• (leave blank to keep current)' : ''}
							required={!detail.sso?.client_secret_set}
							class="mt-1 block w-full rounded-lg border border-gray-300 px-3.5 py-2.5 text-sm text-gray-900 shadow-sm placeholder:text-gray-400 focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors font-mono"
						/>
						<p class="mt-1 text-xs text-gray-400">Sealed server-side with AES-256-GCM. Never returned in any API read.</p>
					</div>

					<div>
						<label for="sso-redirect" class="block text-sm font-medium text-gray-700">Redirect URI to register with your IdP</label>
						<input
							id="sso-redirect"
							type="url"
							bind:value={ssoRedirectURL}
							placeholder="https://api.eurobase.app/platform/auth/sso/callback"
							required
							class="mt-1 block w-full rounded-lg border border-gray-300 px-3.5 py-2.5 text-sm text-gray-900 shadow-sm placeholder:text-gray-400 focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors font-mono"
						/>
						<p class="mt-1 text-xs text-gray-400">Register this exact URL as a redirect / callback URI in your IdP's app configuration.</p>
					</div>

					<div class="flex justify-end pt-2">
						<button
							type="submit"
							disabled={ssoSaving}
							class="inline-flex items-center gap-2 rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 transition-colors disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
						>
							{ssoSaving ? 'Saving…' : 'Save Configuration'}
						</button>
					</div>
				</form>

				<!-- SSO enforcement toggle: refuses password-authenticated
				     sessions at every org-owned access site once enabled.
				     Disabled until OIDC config exists (server-side guard
				     mirrors this; refuses toggle to true otherwise). -->
				<div class="mt-8 rounded-lg border border-amber-200 bg-amber-50/50 p-4">
					<div class="flex items-start gap-3">
						<div class="flex-1">
							<h3 class="text-sm font-semibold text-gray-900">Require SSO for all members</h3>
							<p class="mt-1 text-xs text-gray-600">
								When enabled, members must sign in via <strong>this org's SSO</strong> to see any of its projects. Password login for members is refused with immediate effect — including your own if you haven't signed in via SSO yet.
							</p>
							{#if !detail.sso}
								<p class="mt-2 text-xs text-red-700">
									Configure OIDC above before enabling this — otherwise nobody (including admins) can sign in.
								</p>
							{/if}
							{#if ssoRequiredError}
								<p class="mt-2 text-xs text-red-700">{ssoRequiredError}</p>
							{/if}
						</div>
						<label class="inline-flex cursor-pointer items-center gap-2">
							<input
								type="checkbox"
								bind:checked={ssoRequired}
								disabled={!detail.sso || ssoRequiredSaving}
								onchange={handleSSORequiredToggle}
								class="h-4 w-4 rounded border-gray-300 text-amber-600 focus:ring-amber-500 disabled:opacity-40 disabled:cursor-not-allowed"
							/>
							<span class="text-xs font-medium text-gray-700">{ssoRequired ? 'Required' : 'Optional'}</span>
						</label>
					</div>
				</div>
			</section>
		{:else if detail.sso}
			<!-- Member view: SSO status only, no config edit. -->
			<section class="mt-8 rounded-xl border border-gray-200 bg-white p-6 shadow-sm">
				<h2 class="text-lg font-semibold text-gray-900">Single Sign-On</h2>
				<p class="mt-2 text-sm text-gray-500">SSO is configured for this organization ({detail.sso.provider}). Sign in from the login page using your work email.</p>
			</section>
		{/if}
	{/if}
</div>

{#if showInvite}
	<!-- svelte-ignore a11y_click_events_have_key_events -->
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<div class="absolute inset-0 bg-black/50" onclick={() => (showInvite = false)}></div>
		<div class="relative w-full max-w-md rounded-2xl bg-white p-6 shadow-xl mx-4">
			<h2 class="text-lg font-semibold text-gray-900">Invite Member</h2>
			<p class="mt-1 text-sm text-gray-500">
				Invited users must already have a Eurobase account. SSO logins are gated on the invite — random emails cannot join even with a valid IdP token.
			</p>

			{#if inviteError}
				<div class="mt-4 rounded-lg bg-red-50 border border-red-200 p-3 text-sm text-red-700">{inviteError}</div>
			{/if}

			<form onsubmit={handleInvite} class="mt-5 space-y-4">
				<div>
					<label for="invite-email" class="block text-sm font-medium text-gray-700">Email</label>
					<input
						id="invite-email"
						type="email"
						bind:value={inviteEmail}
						required
						class="mt-1 block w-full rounded-lg border border-gray-300 px-3.5 py-2.5 text-sm text-gray-900 shadow-sm focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
					/>
				</div>
				<div>
					<label for="invite-role" class="block text-sm font-medium text-gray-700">Role</label>
					<select
						id="invite-role"
						bind:value={inviteRole}
						class="mt-1 block w-full rounded-lg border border-gray-300 px-3.5 py-2.5 text-sm text-gray-900 shadow-sm focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
					>
						<option value="member">Member</option>
						<option value="admin">Admin</option>
					</select>
				</div>

				<div class="flex justify-end gap-3 pt-2">
					<button
						type="button"
						onclick={() => (showInvite = false)}
						class="rounded-lg border border-gray-300 px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors cursor-pointer"
					>
						Cancel
					</button>
					<button
						type="submit"
						disabled={inviting || !inviteEmail.trim()}
						class="rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
					>
						{inviting ? 'Inviting…' : 'Invite'}
					</button>
				</div>
			</form>
		</div>
	</div>
{/if}

{#if pendingRemove}
	<!-- svelte-ignore a11y_click_events_have_key_events -->
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div class="fixed inset-0 z-50 flex items-center justify-center" role="dialog" aria-modal="true" aria-labelledby="remove-title">
		<div class="absolute inset-0 bg-black/50" onclick={cancelRemove}></div>
		<div class="relative w-full max-w-md rounded-2xl bg-white p-6 shadow-xl mx-4">
			<h2 id="remove-title" class="text-lg font-semibold text-gray-900">Remove member</h2>
			<p class="mt-2 text-sm text-gray-600">
				Remove <span class="font-medium text-gray-900">{pendingRemove.email}</span> from the organization? They'll lose access to every project owned by this org.
			</p>

			{#if removeError}
				<div class="mt-4 rounded-lg bg-red-50 border border-red-200 p-3 text-sm text-red-700">{removeError}</div>
			{/if}

			<div class="mt-6 flex justify-end gap-3">
				<button
					type="button"
					onclick={cancelRemove}
					disabled={!!removingId}
					class="rounded-lg border border-gray-300 px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-50 disabled:cursor-not-allowed transition-colors cursor-pointer"
				>
					Cancel
				</button>
				<button
					type="button"
					onclick={confirmRemove}
					disabled={!!removingId}
					class="rounded-lg bg-red-600 px-4 py-2 text-sm font-semibold text-white shadow-sm hover:bg-red-700 disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
				>
					{removingId ? 'Removing…' : 'Remove'}
				</button>
			</div>
		</div>
	</div>
{/if}
