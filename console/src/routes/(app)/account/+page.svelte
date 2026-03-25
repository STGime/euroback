<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { user, logout } from '$lib/stores.js';
	import { api, type PlatformProfile } from '$lib/api.js';

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
	});

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

	<!-- Card 4: Session -->
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
