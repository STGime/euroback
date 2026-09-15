<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { user } from '$lib/stores.js';
	import { api } from '$lib/api.js';

	type State = 'verifying' | 'success' | 'error';
	let state = $state<State>('verifying');
	let errorMsg = $state('');
	let resendEmail = $state('');
	let resending = $state(false);
	let resent = $state(false);

	const token = $derived($page.url.searchParams.get('token') ?? '');

	async function redirectAfterLogin() {
		try {
			const list = await api.listProjects();
			await goto(list.length === 0 ? '/onboarding' : '/projects');
		} catch {
			await goto('/projects');
		}
	}

	async function handleResend(e: Event) {
		e.preventDefault();
		if (!resendEmail.trim()) return;
		resending = true;
		try {
			await api.resendVerification(resendEmail.trim());
			resent = true;
		} catch {
			resent = true; // resend always 200s server-side
		} finally {
			resending = false;
		}
	}

	onMount(async () => {
		if (!token) {
			state = 'error';
			errorMsg = 'This verification link is missing its token. Please use the link from your email.';
			return;
		}
		try {
			const resp = await api.verifyEmail(token);
			// Verifying also signs the user in — persist the session and
			// send them into the app.
			user.set({ token: resp.access_token, email: resp.user.email });
			state = 'success';
			await redirectAfterLogin();
		} catch (err) {
			state = 'error';
			errorMsg = err instanceof Error ? err.message : 'Verification failed.';
		}
	});
</script>

<svelte:head>
	<title>Verify Email - Eurobase Console</title>
</svelte:head>

<div class="flex min-h-screen items-center justify-center bg-gray-50 p-8">
	<div class="w-full max-w-sm">
		<div class="mb-8 text-center">
			<div class="inline-flex items-center gap-2">
				<div class="flex h-8 w-8 items-center justify-center rounded-md bg-eurobase-600">
					<svg class="h-5 w-5 text-white" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M9 12.75 11.25 15 15 9.75m-3-7.036A11.959 11.959 0 0 1 3.598 6 11.99 11.99 0 0 0 3 9.749c0 5.592 3.824 10.29 9 11.623 5.176-1.332 9-6.03 9-11.622 0-1.31-.21-2.571-.598-3.751h-.152c-3.196 0-6.1-1.248-8.25-3.285Z" />
					</svg>
				</div>
				<span class="text-xl font-bold text-gray-900">Eurobase</span>
			</div>
		</div>

		<div class="rounded-xl border border-gray-200 bg-white p-8 shadow-sm text-center">
			{#if state === 'verifying'}
				<p class="text-sm text-gray-600">Verifying your email…</p>
			{:else if state === 'success'}
				<h1 class="text-lg font-semibold text-gray-900">Email verified</h1>
				<p class="mt-2 text-sm text-gray-600">Your account is active. Redirecting you to the console…</p>
			{:else}
				<h1 class="text-lg font-semibold text-gray-900">Verification failed</h1>
				<p class="mt-2 text-sm text-red-700">{errorMsg}</p>
				<form onsubmit={handleResend} class="mt-6 space-y-3 text-left">
					<label for="resend-email" class="block text-sm font-medium text-gray-700">Resend the verification link</label>
					<input
						id="resend-email"
						type="email"
						bind:value={resendEmail}
						required
						placeholder="you@company.eu"
						class="block w-full rounded-lg border border-gray-300 px-3.5 py-2.5 text-sm text-gray-900 shadow-sm placeholder:text-gray-400 focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none"
					/>
					<button
						type="submit"
						disabled={resending || resent}
						class="w-full rounded-lg bg-eurobase-600 px-4 py-2.5 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
					>
						{resent ? 'Sent — check your inbox' : resending ? 'Sending…' : 'Resend link'}
					</button>
				</form>
				<div class="mt-4">
					<a href="/login" class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium">Back to sign in</a>
				</div>
			{/if}
		</div>
	</div>
</div>
