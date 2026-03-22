<script lang="ts">
	import { goto } from '$app/navigation';
	import { user } from '$lib/stores.js';
	import { api } from '$lib/api.js';

	let email = $state('');
	let password = $state('');
	let isSignUp = $state(false);
	let submitting = $state(false);
	let error = $state('');

	async function redirectAfterLogin() {
		try {
			const list = await api.listProjects();
			await goto(list.length === 0 ? '/onboarding' : '/projects');
		} catch {
			await goto('/projects');
		}
	}

	async function handleSubmit(e: Event) {
		e.preventDefault();
		error = '';
		submitting = true;
		try {
			const result = isSignUp
				? await api.signUp(email, password)
				: await api.signIn(email, password);
			user.set({ token: result.access_token, email });
			await redirectAfterLogin();
		} catch (err) {
			error = err instanceof Error ? err.message : 'Authentication failed';
		} finally {
			submitting = false;
		}
	}
</script>

<svelte:head>
	<title>{isSignUp ? 'Sign Up' : 'Sign In'} - Eurobase Console</title>
</svelte:head>

<div class="flex min-h-screen">
	<!-- Left panel: branding -->
	<div class="hidden lg:flex lg:w-1/2 bg-eurobase-900 text-white flex-col justify-between p-12">
		<div>
			<div class="flex items-center gap-3">
				<div class="flex h-10 w-10 items-center justify-center rounded-lg bg-eurobase-700">
					<svg class="h-6 w-6 text-eurobase-200" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M9 12.75 11.25 15 15 9.75m-3-7.036A11.959 11.959 0 0 1 3.598 6 11.99 11.99 0 0 0 3 9.749c0 5.592 3.824 10.29 9 11.623 5.176-1.332 9-6.03 9-11.622 0-1.31-.21-2.571-.598-3.751h-.152c-3.196 0-6.1-1.248-8.25-3.285Z" />
					</svg>
				</div>
				<span class="text-2xl font-bold tracking-tight">Eurobase</span>
			</div>
			<p class="mt-4 text-lg text-eurobase-200">EU-Sovereign Backend-as-a-Service</p>
		</div>

		<div class="space-y-8">
			<div class="space-y-4">
				<div class="flex items-start gap-3">
					<div class="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-eurobase-700">
						<svg class="h-3.5 w-3.5 text-eurobase-300" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" />
						</svg>
					</div>
					<div>
						<p class="font-medium">Zero US CLOUD Act exposure</p>
						<p class="text-sm text-eurobase-300">All infrastructure hosted exclusively within EU member states</p>
					</div>
				</div>
				<div class="flex items-start gap-3">
					<div class="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-eurobase-700">
						<svg class="h-3.5 w-3.5 text-eurobase-300" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" />
						</svg>
					</div>
					<div>
						<p class="font-medium">GDPR-native by design</p>
						<p class="text-sm text-eurobase-300">Built for compliance-sensitive B2B verticals</p>
					</div>
				</div>
				<div class="flex items-start gap-3">
					<div class="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-eurobase-700">
						<svg class="h-3.5 w-3.5 text-eurobase-300" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" />
						</svg>
					</div>
					<div>
						<p class="font-medium">Built-in authentication</p>
						<p class="text-sm text-eurobase-300">EU-sovereign auth for your apps — no external dependencies</p>
					</div>
				</div>
			</div>
		</div>

		<div class="flex items-center gap-2 text-sm text-eurobase-400">
			<svg class="h-4 w-4" viewBox="0 0 24 24" fill="currentColor">
				<circle cx="12" cy="12" r="10" fill="none" stroke="currentColor" stroke-width="1.5"/>
				<circle cx="12" cy="5" r="1" />
				<circle cx="15.5" cy="6.3" r="1" />
				<circle cx="17.7" cy="9.5" r="1" />
				<circle cx="17.7" cy="14.5" r="1" />
				<circle cx="15.5" cy="17.7" r="1" />
				<circle cx="12" cy="19" r="1" />
				<circle cx="8.5" cy="17.7" r="1" />
				<circle cx="6.3" cy="14.5" r="1" />
				<circle cx="6.3" cy="9.5" r="1" />
				<circle cx="8.5" cy="6.3" r="1" />
			</svg>
			<span>Infrastructure powered by Scaleway (Paris, FR)</span>
		</div>
	</div>

	<!-- Right panel: auth -->
	<div class="flex w-full lg:w-1/2 items-center justify-center p-8 bg-gray-50">
		<div class="w-full max-w-sm">
			<!-- Mobile logo -->
			<div class="mb-8 lg:hidden text-center">
				<div class="inline-flex items-center gap-2">
					<div class="flex h-8 w-8 items-center justify-center rounded-md bg-eurobase-600">
						<svg class="h-5 w-5 text-white" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M9 12.75 11.25 15 15 9.75m-3-7.036A11.959 11.959 0 0 1 3.598 6 11.99 11.99 0 0 0 3 9.749c0 5.592 3.824 10.29 9 11.623 5.176-1.332 9-6.03 9-11.622 0-1.31-.21-2.571-.598-3.751h-.152c-3.196 0-6.1-1.248-8.25-3.285Z" />
						</svg>
					</div>
					<span class="text-xl font-bold text-gray-900">Eurobase</span>
				</div>
				<p class="mt-1 text-sm text-gray-500">EU-Sovereign Backend-as-a-Service</p>
			</div>

			<div class="rounded-xl border border-gray-200 bg-white p-8 shadow-sm">
				<h2 class="text-xl font-semibold text-gray-900">
					{isSignUp ? 'Create your account' : 'Sign in to your account'}
				</h2>
				<p class="mt-1 text-sm text-gray-500">
					{isSignUp ? 'Get started with Eurobase' : 'Access your Eurobase console'}
				</p>

				{#if error}
					<div class="mt-4 rounded-lg bg-red-50 border border-red-200 p-3 text-sm text-red-700">
						{error}
					</div>
				{/if}

				<form onsubmit={handleSubmit} class="mt-6 space-y-4">
					<div>
						<label for="email" class="block text-sm font-medium text-gray-700">Email address</label>
						<input
							id="email"
							type="email"
							bind:value={email}
							required
							placeholder="you@company.eu"
							class="mt-1 block w-full rounded-lg border border-gray-300 px-3.5 py-2.5 text-sm text-gray-900 shadow-sm placeholder:text-gray-400 focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
						/>
					</div>

					<div>
						<label for="password" class="block text-sm font-medium text-gray-700">Password</label>
						<input
							id="password"
							type="password"
							bind:value={password}
							required
							minlength="8"
							placeholder={isSignUp ? 'At least 8 characters' : ''}
							class="mt-1 block w-full rounded-lg border border-gray-300 px-3.5 py-2.5 text-sm text-gray-900 shadow-sm placeholder:text-gray-400 focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
						/>
					</div>

					<button
						type="submit"
						disabled={submitting}
						class="w-full rounded-lg bg-eurobase-600 px-4 py-2.5 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
					>
						{submitting ? (isSignUp ? 'Creating account...' : 'Signing in...') : (isSignUp ? 'Create Account' : 'Sign In')}
					</button>
				</form>

				<div class="mt-4 text-center text-sm text-gray-500">
					{#if isSignUp}
						Already have an account?
						<button onclick={() => { isSignUp = false; error = ''; }} class="text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">Sign in</button>
					{:else}
						Don't have an account?
						<button onclick={() => { isSignUp = true; error = ''; }} class="text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">Sign up</button>
					{/if}
				</div>
			</div>

			<p class="mt-6 text-center text-xs text-gray-400">
				All data stored exclusively in EU datacenters under EU law.
				<br />
				No US CLOUD Act exposure. GDPR compliant by design.
			</p>
		</div>
	</div>
</div>
