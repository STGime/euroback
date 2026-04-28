<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { user, logout } from '$lib/stores.js';
	import { api, type PlatformProfile, type PersonalAccessToken } from '$lib/api.js';

	let profile = $state<PlatformProfile | null>(null);
	let profileLoading = $state(true);
	let profileError = $state('');

	// Display name
	let displayName = $state('');
	let nameSaving = $state(false);
	let nameSuccess = $state('');
	let nameError = $state('');

	// Change password
	let currentPassword = $state('');
	let newPassword = $state('');
	let confirmPassword = $state('');
	let passwordSaving = $state(false);
	let passwordSuccess = $state('');
	let passwordError = $state('');

	// Personal Access Tokens
	let tokens = $state<PersonalAccessToken[]>([]);
	let tokensLoading = $state(true);
	let tokensError = $state('');
	let showCreateToken = $state(false);
	let newTokenName = $state('');
	let newTokenExpiry = $state(''); // YYYY-MM-DD or empty
	let creatingToken = $state(false);
	let createTokenError = $state('');
	let plaintextToken = $state(''); // shown once after creation
	let copiedToken = $state(false);
	let revokingTokenId = $state('');

	// Delete account
	let deleteEmail = $state('');
	let deleting = $state(false);
	let deleteError = $state('');

	onMount(async () => {
		try {
			profile = await api.getProfile();
			displayName = profile.display_name ?? '';
		} catch (err) {
			profileError = err instanceof Error ? err.message : 'Failed to load profile';
		} finally {
			profileLoading = false;
		}
		await loadTokens();
	});

	async function loadTokens() {
		tokensLoading = true;
		tokensError = '';
		try {
			tokens = await api.listPATs();
		} catch (err) {
			tokensError = err instanceof Error ? err.message : 'Failed to load tokens';
		} finally {
			tokensLoading = false;
		}
	}

	async function handleCreateToken() {
		createTokenError = '';
		if (!newTokenName.trim()) {
			createTokenError = 'Name is required.';
			return;
		}
		creatingToken = true;
		try {
			const expiresAt = newTokenExpiry ? new Date(newTokenExpiry + 'T00:00:00Z').toISOString() : null;
			const res = await api.createPAT(newTokenName.trim(), expiresAt);
			plaintextToken = res.token;
			tokens = [res.pat, ...tokens];
			newTokenName = '';
			newTokenExpiry = '';
			showCreateToken = false;
		} catch (err) {
			createTokenError = err instanceof Error ? err.message : 'Failed to create token';
		} finally {
			creatingToken = false;
		}
	}

	async function handleRevokeToken(id: string) {
		revokingTokenId = id;
		try {
			await api.revokePAT(id);
			tokens = tokens.filter(t => t.id !== id);
		} catch (err) {
			tokensError = err instanceof Error ? err.message : 'Failed to revoke token';
		} finally {
			revokingTokenId = '';
		}
	}

	async function copyPlaintextToken() {
		try {
			await navigator.clipboard.writeText(plaintextToken);
			copiedToken = true;
			setTimeout(() => { copiedToken = false; }, 2000);
		} catch {
			// silently fail
		}
	}

	function dismissPlaintextToken() {
		plaintextToken = '';
		copiedToken = false;
	}

	function tokenStatus(t: PersonalAccessToken): { label: string; klass: string } {
		if (t.expires_at) {
			const exp = new Date(t.expires_at);
			if (exp < new Date()) return { label: 'expired', klass: 'bg-red-100 text-red-700' };
			return { label: `expires ${exp.toLocaleDateString('en-GB')}`, klass: 'bg-gray-100 text-gray-700' };
		}
		return { label: 'no expiry', klass: 'bg-gray-100 text-gray-700' };
	}

	function formatDate(iso: string): string {
		return new Date(iso).toLocaleDateString('en-GB', {
			day: 'numeric',
			month: 'long',
			year: 'numeric'
		});
	}

	async function saveDisplayName() {
		nameError = '';
		nameSuccess = '';
		nameSaving = true;
		try {
			await api.updateDisplayName(displayName);
			nameSuccess = 'Display name saved.';
			if (profile) profile.display_name = displayName;
		} catch (err) {
			nameError = err instanceof Error ? err.message : 'Failed to save';
		} finally {
			nameSaving = false;
		}
	}

	async function handleChangePassword() {
		passwordError = '';
		passwordSuccess = '';

		if (newPassword.length < 8) {
			passwordError = 'New password must be at least 8 characters.';
			return;
		}
		if (newPassword !== confirmPassword) {
			passwordError = 'Passwords do not match.';
			return;
		}

		passwordSaving = true;
		try {
			await api.changePassword(currentPassword, newPassword);
			passwordSuccess = 'Password updated.';
			currentPassword = '';
			newPassword = '';
			confirmPassword = '';
		} catch (err) {
			passwordError = err instanceof Error ? err.message : 'Failed to change password';
		} finally {
			passwordSaving = false;
		}
	}

	async function handleDeleteAccount() {
		deleteError = '';
		deleting = true;
		try {
			await api.deleteAccount(deleteEmail);
			logout();
			goto('/login');
		} catch (err) {
			deleteError = err instanceof Error ? err.message : 'Failed to delete account';
		} finally {
			deleting = false;
		}
	}

	function handleSignOut() {
		logout();
		goto('/login');
	}

	const planColors: Record<string, string> = {
		free: 'bg-gray-100 text-gray-700',
		pro: 'bg-eurobase-100 text-eurobase-700',
		team: 'bg-blue-100 text-blue-700',
		enterprise: 'bg-purple-100 text-purple-700'
	};
</script>

<div class="mx-auto max-w-xl space-y-6">
	<div>
		<h2 class="text-lg font-semibold text-gray-900">Account</h2>
		<p class="text-sm text-gray-500">Manage your Eurobase account.</p>
	</div>

	<!-- Card 1: Account Info -->
	<div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
		<div class="px-5 py-3 border-b border-gray-100">
			<h3 class="text-sm font-semibold text-gray-900">Account Info</h3>
		</div>
		<div class="px-5 py-4">
			{#if profileLoading}
				<p class="text-sm text-gray-400">Loading...</p>
			{:else if profileError}
				<p class="text-sm text-red-600">{profileError}</p>
			{:else if profile}
				<dl class="grid grid-cols-2 gap-y-3 text-sm">
					<dt class="text-gray-500">Email</dt>
					<dd class="font-mono text-gray-700">{profile.email}</dd>

					<dt class="text-gray-500">Plan</dt>
					<dd>
						<span class="inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium {planColors[profile.plan] ?? 'bg-gray-100 text-gray-700'}">
							{profile.plan}
						</span>
					</dd>

					<dt class="text-gray-500">Member since</dt>
					<dd class="text-gray-700">{formatDate(profile.created_at)}</dd>

					<dt class="text-gray-500">Last sign-in</dt>
					<dd class="text-gray-700">{profile.last_sign_in_at ? formatDate(profile.last_sign_in_at) : 'N/A'}</dd>
				</dl>
			{/if}
		</div>
	</div>

	<!-- Card 2: Display Name -->
	<div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
		<div class="px-5 py-3 border-b border-gray-100">
			<h3 class="text-sm font-semibold text-gray-900">Display Name</h3>
		</div>
		<div class="px-5 py-4 space-y-3">
			<input
				type="text"
				bind:value={displayName}
				maxlength={100}
				placeholder="Your display name"
				class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 focus:border-eurobase-500 focus:ring-1 focus:ring-eurobase-500 outline-none"
			/>
			{#if nameError}
				<p class="text-xs text-red-600">{nameError}</p>
			{/if}
			{#if nameSuccess}
				<p class="text-xs text-green-600">{nameSuccess}</p>
			{/if}
			<button
				type="button"
				disabled={nameSaving}
				onclick={saveDisplayName}
				class="inline-flex items-center rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 transition-colors disabled:opacity-50 cursor-pointer"
			>
				{nameSaving ? 'Saving...' : 'Save'}
			</button>
		</div>
	</div>

	<!-- Card 3: Change Password -->
	<div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
		<div class="px-5 py-3 border-b border-gray-100">
			<h3 class="text-sm font-semibold text-gray-900">Change Password</h3>
		</div>
		<div class="px-5 py-4 space-y-3">
			<input
				type="password"
				bind:value={currentPassword}
				placeholder="Current password"
				class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 focus:border-eurobase-500 focus:ring-1 focus:ring-eurobase-500 outline-none"
			/>
			<input
				type="password"
				bind:value={newPassword}
				placeholder="New password (min 8 characters)"
				class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 focus:border-eurobase-500 focus:ring-1 focus:ring-eurobase-500 outline-none"
			/>
			<input
				type="password"
				bind:value={confirmPassword}
				placeholder="Confirm new password"
				class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 focus:border-eurobase-500 focus:ring-1 focus:ring-eurobase-500 outline-none"
			/>
			{#if passwordError}
				<p class="text-xs text-red-600">{passwordError}</p>
			{/if}
			{#if passwordSuccess}
				<p class="text-xs text-green-600">{passwordSuccess}</p>
			{/if}
			<button
				type="button"
				disabled={passwordSaving}
				onclick={handleChangePassword}
				class="inline-flex items-center rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 transition-colors disabled:opacity-50 cursor-pointer"
			>
				{passwordSaving ? 'Updating...' : 'Update Password'}
			</button>
		</div>
	</div>

	<!-- Card 4: Personal Access Tokens -->
	<div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
		<div class="px-5 py-3 border-b border-gray-100 flex items-center justify-between">
			<div>
				<h3 class="text-sm font-semibold text-gray-900">Personal Access Tokens</h3>
				<p class="mt-0.5 text-xs text-gray-500">Long-lived bearer tokens for the MCP server, CLI, and CI. Authenticate as you, but never carry superadmin powers.</p>
			</div>
			<button
				type="button"
				onclick={() => { showCreateToken = true; createTokenError = ''; }}
				class="inline-flex items-center rounded-lg bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 transition-colors cursor-pointer"
			>
				+ New token
			</button>
		</div>

		<!-- Plaintext token banner (shown once after creation) -->
		{#if plaintextToken}
			<div class="border-b border-amber-200 bg-amber-50 px-5 py-4">
				<p class="text-sm font-semibold text-amber-900">Copy your new token now</p>
				<p class="mt-1 text-xs text-amber-800">This is the only time it will be shown. Store it in a password manager or a <code class="rounded bg-amber-100 px-1 font-mono">.env</code> file.</p>
				<div class="mt-3 flex items-center gap-2">
					<code class="flex-1 rounded-lg bg-gray-900 px-3 py-2 text-xs font-mono text-gray-100 overflow-x-auto whitespace-nowrap">{plaintextToken}</code>
					<button
						type="button"
						onclick={copyPlaintextToken}
						class="rounded-md border border-amber-300 bg-white px-3 py-2 text-xs font-medium text-amber-900 hover:bg-amber-50 cursor-pointer"
					>{copiedToken ? 'Copied!' : 'Copy'}</button>
					<button
						type="button"
						onclick={dismissPlaintextToken}
						class="rounded-md px-2 py-2 text-xs text-amber-800 hover:bg-amber-100 cursor-pointer"
					>Dismiss</button>
				</div>
			</div>
		{/if}

		<div class="px-5 py-4">
			{#if tokensLoading}
				<p class="text-sm text-gray-400">Loading...</p>
			{:else if tokensError}
				<p class="text-sm text-red-600">{tokensError}</p>
			{:else if tokens.length === 0}
				<p class="text-sm text-gray-500">No tokens yet. Create one to use Eurobase from MCP, the CLI, or CI.</p>
			{:else}
				<div class="space-y-2">
					{#each tokens as t (t.id)}
						{@const st = tokenStatus(t)}
						<div class="flex items-center justify-between rounded-lg border border-gray-200 px-3 py-2">
							<div class="min-w-0 flex-1">
								<div class="flex items-center gap-2">
									<p class="text-sm font-medium text-gray-900 truncate">{t.name}</p>
									<span class="inline-flex items-center rounded-full px-2 py-0.5 text-[10px] font-medium {st.klass}">{st.label}</span>
								</div>
								<p class="mt-0.5 text-xs font-mono text-gray-500">{t.prefix}…</p>
								<p class="mt-0.5 text-[11px] text-gray-400">
									Created {new Date(t.created_at).toLocaleDateString('en-GB')}
									{#if t.last_used_at} · Last used {new Date(t.last_used_at).toLocaleDateString('en-GB')}{:else} · Never used{/if}
								</p>
							</div>
							<button
								type="button"
								disabled={revokingTokenId === t.id}
								onclick={() => handleRevokeToken(t.id)}
								class="ml-3 rounded-md border border-gray-200 px-2 py-1 text-xs font-medium text-red-600 hover:bg-red-50 disabled:opacity-50 cursor-pointer"
							>{revokingTokenId === t.id ? 'Revoking…' : 'Revoke'}</button>
						</div>
					{/each}
				</div>
			{/if}
		</div>

		<!-- Create-token modal -->
		{#if showCreateToken}
			<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
				<div class="w-full max-w-md rounded-xl bg-white shadow-lg">
					<div class="border-b border-gray-100 px-5 py-3">
						<h4 class="text-sm font-semibold text-gray-900">Create personal access token</h4>
					</div>
					<div class="space-y-3 px-5 py-4">
						<div class="rounded-lg border border-gray-200 bg-gray-50 px-3 py-2 text-xs text-gray-700 leading-relaxed">
							<p class="font-medium text-gray-800">What this token can do:</p>
							<ul class="mt-1 ml-4 list-disc space-y-0.5">
								<li>Read and write any project you own or are a member of</li>
								<li>Use the MCP server, SDK, and platform API on your behalf</li>
							</ul>
							<p class="mt-2 font-medium text-gray-800">What it cannot do:</p>
							<ul class="mt-1 ml-4 list-disc space-y-0.5">
								<li>Access superadmin endpoints (allowlist, cross-tenant project list)</li>
								<li>Create more tokens (sign in to the console for that)</li>
								<li>Change your password or delete your account</li>
							</ul>
						</div>
						<div>
							<label for="token-name" class="block text-xs font-medium text-gray-700 mb-1">Name</label>
							<input
								id="token-name"
								type="text"
								bind:value={newTokenName}
								maxlength={100}
								placeholder="e.g. my laptop, ci-prod"
								class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 focus:border-eurobase-500 focus:ring-1 focus:ring-eurobase-500 outline-none"
							/>
						</div>
						<div>
							<label for="token-expiry" class="block text-xs font-medium text-gray-700 mb-1">Expiry <span class="text-gray-400">(optional)</span></label>
							<input
								id="token-expiry"
								type="date"
								bind:value={newTokenExpiry}
								min={new Date(Date.now() + 86400000).toISOString().slice(0, 10)}
								class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 focus:border-eurobase-500 focus:ring-1 focus:ring-eurobase-500 outline-none"
							/>
							<p class="mt-1 text-[11px] text-gray-500">Leave blank for no expiry.</p>
						</div>
						{#if createTokenError}
							<p class="text-xs text-red-600">{createTokenError}</p>
						{/if}
					</div>
					<div class="flex justify-end gap-2 border-t border-gray-100 px-5 py-3">
						<button
							type="button"
							onclick={() => { showCreateToken = false; newTokenName = ''; newTokenExpiry = ''; createTokenError = ''; }}
							class="rounded-lg border border-gray-200 px-3 py-1.5 text-sm font-medium text-gray-700 hover:bg-gray-50 cursor-pointer"
						>Cancel</button>
						<button
							type="button"
							disabled={creatingToken}
							onclick={handleCreateToken}
							class="rounded-lg bg-eurobase-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-eurobase-700 disabled:opacity-50 cursor-pointer"
						>{creatingToken ? 'Creating…' : 'Create token'}</button>
					</div>
				</div>
			</div>
		{/if}
	</div>

	<!-- Card 5: Session -->
	<div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
		<div class="px-5 py-3 border-b border-gray-100">
			<h3 class="text-sm font-semibold text-gray-900">Session</h3>
		</div>
		<div class="px-5 py-4">
			<button
				type="button"
				class="cursor-pointer inline-flex items-center gap-2 rounded-lg bg-gray-100 px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-200 transition-colors"
				onclick={handleSignOut}
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" d="M15.75 9V5.25A2.25 2.25 0 0 0 13.5 3h-6a2.25 2.25 0 0 0-2.25 2.25v13.5A2.25 2.25 0 0 0 7.5 21h6a2.25 2.25 0 0 0 2.25-2.25V15m3 0 3-3m0 0-3-3m3 3H9" />
				</svg>
				Sign Out
			</button>
		</div>
	</div>

	<!-- Card 5: Danger Zone -->
	<div class="rounded-xl border border-red-200 bg-white overflow-hidden">
		<div class="px-5 py-3 border-b border-red-100">
			<h3 class="text-sm font-semibold text-red-700">Danger Zone</h3>
		</div>
		<div class="px-5 py-4 space-y-3">
			<p class="text-sm text-gray-600">
				Permanently delete your account and all associated data. You must delete all projects first. This action cannot be undone.
			</p>
			<input
				type="email"
				bind:value={deleteEmail}
				placeholder="Type your email to confirm"
				class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 focus:border-red-500 focus:ring-1 focus:ring-red-500 outline-none"
			/>
			{#if deleteError}
				<p class="text-xs text-red-600">{deleteError}</p>
			{/if}
			<button
				type="button"
				disabled={deleting || !deleteEmail}
				onclick={handleDeleteAccount}
				class="inline-flex items-center rounded-lg bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700 transition-colors disabled:opacity-50 cursor-pointer"
			>
				{deleting ? 'Deleting...' : 'Delete my account'}
			</button>
		</div>
	</div>
</div>
