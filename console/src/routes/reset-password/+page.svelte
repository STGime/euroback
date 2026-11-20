<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { api } from '$lib/api.js';

	let password = $state('');
	let confirmPassword = $state('');
	let submitting = $state(false);
	let error = $state('');
	let success = $state(false);

	const token = $derived($page.url.searchParams.get('token') ?? '');

	async function handleSubmit(e: Event) {
		e.preventDefault();
		error = '';

		if (!token) {
			error = 'Missing reset token. Please use the link from your email.';
			return;
		}

		if (password !== confirmPassword) {
			error = 'Passwords do not match.';
			return;
		}

		if (password.length < 8) {
			error = 'Password must be at least 8 characters.';
			return;
		}

		submitting = true;
		try {
			await api.platformResetPassword(token, password);
			success = true;
			setTimeout(() => goto('/login'), 3000);
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to reset password';
		} finally {
			submitting = false;
		}
	}
</script>

<svelte:head>
	<title>Reset Password - Eurobase Console</title>
</svelte:head>

<div class="flex min-h-screen items-center justify-center bg-gray-50 p-8">
	<div class="w-full max-w-sm">
		<div class="mb-8 text-center">
			<div class="inline-flex items-center gap-2">
				<span class="text-xl font-bold text-gray-900">Eurobase</span>
			</div>
		</div>

		<div class="rounded-xl border border-gray-200 bg-white p-8 shadow-sm">
			<h2 class="text-xl font-semibold text-gray-900">Set new password</h2>
			<p class="mt-1 text-sm text-gray-500">Enter your new password below.</p>

			{#if error}
				<div class="mt-4 rounded-lg bg-red-50 border border-red-200 p-3 text-sm text-red-700">
					{error}
				</div>
			{/if}

			{#if success}
				<div class="mt-4 rounded-lg bg-emerald-50 border border-emerald-200 p-4 text-sm text-emerald-700">
					<p class="font-medium">Password updated!</p>
					<p class="mt-1">Redirecting to sign in...</p>
				</div>
			{:else}
				<form onsubmit={handleSubmit} class="mt-6 space-y-4">
					<div>
						<label for="password" class="block text-sm font-medium text-gray-700">New password</label>
						<input
							id="password"
							type="password" autocomplete="new-password"
							bind:value={password}
							required
							minlength="8"
							placeholder="At least 8 characters"
							class="mt-1 block w-full rounded-lg border border-gray-300 px-3.5 py-2.5 text-sm text-gray-900 shadow-sm placeholder:text-gray-400 focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
						/>
					</div>

					<div>
						<label for="confirm-password" class="block text-sm font-medium text-gray-700">Confirm password</label>
						<input
							id="confirm-password"
							type="password" autocomplete="new-password"
							bind:value={confirmPassword}
							required
							minlength="8"
							class="mt-1 block w-full rounded-lg border border-gray-300 px-3.5 py-2.5 text-sm text-gray-900 shadow-sm placeholder:text-gray-400 focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
						/>
					</div>

					<button
						type="submit"
						disabled={submitting}
						class="w-full rounded-lg bg-eurobase-600 px-4 py-2.5 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
					>
						{submitting ? 'Updating...' : 'Update Password'}
					</button>
				</form>

				<div class="mt-4 text-center">
					<a href="/login" class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium">Back to sign in</a>
				</div>
			{/if}
		</div>
	</div>
</div>
