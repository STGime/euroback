<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { onMount } from 'svelte';
	import { user } from '$lib/stores.js';
	import { api } from '$lib/api.js';

	let status = $state<'loading' | 'review' | 'accepting' | 'success' | 'error' | 'login_required'>('loading');
	let errorMessage = $state('');
	let projectId = $state('');
	let billingHandover = $state(false);
	let token = $state('');

	onMount(() => {
		const t = $page.url.searchParams.get('token');
		if (!t) {
			status = 'error';
			errorMessage = 'No transfer token provided.';
			return;
		}
		token = t;
		// Don't auto-accept — transferring ownership is deliberate. Show a
		// review step the recipient must confirm.
		status = $user ? 'review' : 'login_required';
	});

	async function acceptTransfer() {
		status = 'accepting';
		try {
			const result = await api.acceptTransfer(token);
			projectId = result.project_id;
			billingHandover = result.billing_handover;
			status = 'success';
			setTimeout(() => goto(`/p/${projectId}`), 3000);
		} catch (err) {
			status = 'error';
			errorMessage = err instanceof Error ? err.message : 'Failed to accept the transfer.';
		}
	}

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
	<title>Accept Project Transfer - Eurobase Console</title>
</svelte:head>

<div class="min-h-screen bg-gray-50 flex items-center justify-center px-4">
	<div class="max-w-md w-full">
		<div class="text-center mb-8">
			<a href="/" class="inline-flex items-center gap-2 text-xl font-bold text-gray-900">
				<div class="flex h-8 w-8 items-center justify-center rounded-md bg-eurobase-600">
					<svg class="h-5 w-5 text-white" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M9 12.75 11.25 15 15 9.75m-3-7.036A11.959 11.959 0 0 1 3.598 6 11.99 11.99 0 0 0 3 9.749c0 5.592 3.824 10.29 9 11.623 5.176-1.332 9-6.03 9-11.622 0-1.31-.21-2.571-.598-3.751h-.152c-3.196 0-6.1-1.248-8.25-3.285Z" />
					</svg>
				</div>
				Eurobase
			</a>
		</div>

		<div class="bg-white rounded-xl border border-gray-200 shadow-sm p-8">
			{#if status === 'loading' || status === 'accepting'}
				<div class="text-center">
					<div class="inline-flex items-center gap-3">
						<svg class="h-5 w-5 animate-spin text-eurobase-600" fill="none" viewBox="0 0 24 24">
							<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
							<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
						</svg>
						<span class="text-sm text-gray-600">{status === 'accepting' ? 'Completing transfer…' : 'Loading…'}</span>
					</div>
				</div>
			{:else if status === 'login_required'}
				<div class="text-center space-y-4">
					<div class="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-eurobase-100">
						<svg class="h-6 w-6 text-eurobase-600" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M21.75 6.75v10.5a2.25 2.25 0 0 1-2.25 2.25h-15a2.25 2.25 0 0 1-2.25-2.25V6.75m19.5 0A2.25 2.25 0 0 0 19.5 4.5h-15a2.25 2.25 0 0 0-2.25 2.25m19.5 0v.243a2.25 2.25 0 0 1-1.07 1.916l-7.5 4.615a2.25 2.25 0 0 1-2.36 0L3.32 8.91a2.25 2.25 0 0 1-1.07-1.916V6.75" />
						</svg>
					</div>
					<h2 class="text-lg font-semibold text-gray-900">You've been offered a project</h2>
					<p class="text-sm text-gray-500">Sign in or create an account with the email the offer was sent to, then accept.</p>
					<div class="flex flex-col gap-3 pt-2">
						<button onclick={redirectToLogin} class="w-full rounded-lg bg-eurobase-600 px-4 py-2.5 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 transition-colors cursor-pointer">Sign In</button>
						<button onclick={redirectToSignUp} class="w-full rounded-lg border border-gray-300 px-4 py-2.5 text-sm font-semibold text-gray-700 shadow-sm hover:bg-gray-50 transition-colors cursor-pointer">Create Account</button>
					</div>
				</div>
			{:else if status === 'review'}
				<div class="space-y-4">
					<div class="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-eurobase-100">
						<svg class="h-6 w-6 text-eurobase-600" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M7.5 21 3 16.5m0 0L7.5 12M3 16.5h13.5m0-13.5L21 7.5m0 0L16.5 12M21 7.5H7.5" />
						</svg>
					</div>
					<h2 class="text-center text-lg font-semibold text-gray-900">Accept project ownership</h2>
					<p class="text-sm text-gray-500">
						Accepting makes you the <strong>owner</strong> of this project — you'll manage its settings, members and billing.
						The previous owner stays on as an admin unless they leave.
					</p>
					<p class="text-sm text-gray-500">
						If the project is on a paid plan, it keeps running through the period the previous owner already paid for.
						Once that period ends you set up your own subscription from <strong>Settings &rarr; Billing</strong> to continue on a paid plan
						(there may be a short gap before you subscribe, during which the project is on the Free plan).
					</p>
					<button
						onclick={acceptTransfer}
						class="w-full rounded-lg bg-eurobase-600 px-4 py-2.5 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 transition-colors cursor-pointer"
					>
						Accept and become owner
					</button>
				</div>
			{:else if status === 'success'}
				<div class="text-center space-y-4">
					<div class="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-green-100">
						<svg class="h-6 w-6 text-green-600" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" />
						</svg>
					</div>
					<h2 class="text-lg font-semibold text-gray-900">You're now the owner</h2>
					{#if billingHandover}
						<p class="text-sm text-gray-500">The project stays on its paid plan through the current billing period. Once that period ends, start your own subscription from <strong>Settings → Billing</strong> to keep it on a paid plan — there may be a brief gap on the Free plan until you do.</p>
					{:else}
						<p class="text-sm text-gray-500">Redirecting to your project…</p>
					{/if}
					<a href="/p/{projectId}" class="inline-block text-sm text-eurobase-600 hover:text-eurobase-700 font-medium">Go to project now</a>
				</div>
			{:else if status === 'error'}
				<div class="text-center space-y-4">
					<div class="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-red-100">
						<svg class="h-6 w-6 text-red-600" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
						</svg>
					</div>
					<h2 class="text-lg font-semibold text-gray-900">Unable to accept the transfer</h2>
					<p class="text-sm text-gray-500">{errorMessage}</p>
					<a href="/login" class="inline-block text-sm text-eurobase-600 hover:text-eurobase-700 font-medium">Go to login</a>
				</div>
			{/if}
		</div>
	</div>
</div>
