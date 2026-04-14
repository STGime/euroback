<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { onMount } from 'svelte';
	import { user } from '$lib/stores.js';
	import { api } from '$lib/api.js';

	let status = $state<'loading' | 'success' | 'error' | 'login_required'>('loading');
	let errorMessage = $state('');
	let projectId = $state('');
	let role = $state('');

	onMount(async () => {
		const token = $page.url.searchParams.get('token');
		if (!token) {
			status = 'error';
			errorMessage = 'No invitation token provided.';
			return;
		}

		// If not logged in, redirect to login with a return URL.
		if (!$user) {
			status = 'login_required';
			return;
		}

		// Accept the invitation.
		try {
			const result = await api.acceptInvitation(token);
			projectId = result.project_id;
			role = result.role;
			status = 'success';
			// Redirect to the project after a brief delay.
			setTimeout(() => goto(`/p/${projectId}`), 2000);
		} catch (err) {
			status = 'error';
			errorMessage = err instanceof Error ? err.message : 'Failed to accept invitation.';
		}
	});

	function redirectToLogin() {
		const returnUrl = $page.url.pathname + $page.url.search;
		goto(`/login?redirect=${encodeURIComponent(returnUrl)}`);
	}

	function redirectToSignUp() {
		const returnUrl = $page.url.pathname + $page.url.search;
		goto(`/login?redirect=${encodeURIComponent(returnUrl)}&signup=1`);
	}
</script>

<svelte:head>
	<title>Accept Invitation - Eurobase Console</title>
</svelte:head>

<div class="min-h-screen bg-gray-50 flex items-center justify-center px-4">
	<div class="max-w-md w-full">
		<div class="text-center mb-8">
			<a href="/" class="inline-flex items-center gap-2 text-xl font-bold text-gray-900">
				<img src="/favicon.svg" alt="" class="h-8 w-8" />
				Eurobase
			</a>
		</div>

		<div class="bg-white rounded-xl border border-gray-200 shadow-sm p-8">
			{#if status === 'loading'}
				<div class="text-center">
					<div class="inline-flex items-center gap-3">
						<svg class="h-5 w-5 animate-spin text-eurobase-600" fill="none" viewBox="0 0 24 24">
							<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
							<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
						</svg>
						<span class="text-sm text-gray-600">Accepting invitation...</span>
					</div>
				</div>
			{:else if status === 'login_required'}
				<div class="text-center space-y-4">
					<div class="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-eurobase-100">
						<svg class="h-6 w-6 text-eurobase-600" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M21.75 6.75v10.5a2.25 2.25 0 0 1-2.25 2.25h-15a2.25 2.25 0 0 1-2.25-2.25V6.75m19.5 0A2.25 2.25 0 0 0 19.5 4.5h-15a2.25 2.25 0 0 0-2.25 2.25m19.5 0v.243a2.25 2.25 0 0 1-1.07 1.916l-7.5 4.615a2.25 2.25 0 0 1-2.36 0L3.32 8.91a2.25 2.25 0 0 1-1.07-1.916V6.75" />
						</svg>
					</div>
					<h2 class="text-lg font-semibold text-gray-900">You've been invited to a project</h2>
					<p class="text-sm text-gray-500">Sign in or create an account to accept this invitation.</p>
					<div class="flex flex-col gap-3 pt-2">
						<button
							onclick={redirectToLogin}
							class="w-full rounded-lg bg-eurobase-600 px-4 py-2.5 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 transition-colors cursor-pointer"
						>
							Sign In
						</button>
						<button
							onclick={redirectToSignUp}
							class="w-full rounded-lg border border-gray-300 px-4 py-2.5 text-sm font-semibold text-gray-700 shadow-sm hover:bg-gray-50 transition-colors cursor-pointer"
						>
							Create Account
						</button>
					</div>
				</div>
			{:else if status === 'success'}
				<div class="text-center space-y-4">
					<div class="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-green-100">
						<svg class="h-6 w-6 text-green-600" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" />
						</svg>
					</div>
					<h2 class="text-lg font-semibold text-gray-900">Invitation accepted!</h2>
					<p class="text-sm text-gray-500">You've joined the project as <strong>{role}</strong>. Redirecting to the dashboard...</p>
					<a href="/p/{projectId}" class="inline-block text-sm text-eurobase-600 hover:text-eurobase-700 font-medium">
						Go to project now
					</a>
				</div>
			{:else if status === 'error'}
				<div class="text-center space-y-4">
					<div class="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-red-100">
						<svg class="h-6 w-6 text-red-600" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
						</svg>
					</div>
					<h2 class="text-lg font-semibold text-gray-900">Unable to accept invitation</h2>
					<p class="text-sm text-gray-500">{errorMessage}</p>
					<a href="/login" class="inline-block text-sm text-eurobase-600 hover:text-eurobase-700 font-medium">
						Go to login
					</a>
				</div>
			{/if}
		</div>
	</div>
</div>
