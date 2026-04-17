<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { user } from '$lib/stores.js';
	import { api } from '$lib/api.js';

	let email = $state('');
	let password = $state('');
	let confirmPassword = $state('');
	let showPassword = $state(false);
	let isSignUp = $state($page.url.searchParams.get('signup') === '1');
	let isForgotPassword = $state(false);
	let forgotPasswordSent = $state(false);
	let submitting = $state(false);
	let error = $state('');
	let waitlisted = $state(false);

	function parseError(raw: string): string {
		const jsonMatch = raw.match(/\{"error":"(.+?)"\}/);
		if (jsonMatch) {
			const msg = jsonMatch[1];
			return msg.charAt(0).toUpperCase() + msg.slice(1);
		}
		const prefixMatch = raw.match(/^API \d+: (.+)/);
		if (prefixMatch) return prefixMatch[1];
		return raw;
	}

	async function redirectAfterLogin() {
		const redirectUrl = $page.url.searchParams.get('redirect');
		if (redirectUrl) {
			await goto(redirectUrl);
			return;
		}
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
		if (isSignUp && password !== confirmPassword) {
			error = 'Passwords do not match';
			return;
		}
		submitting = true;
		try {
			if (isForgotPassword) {
				await api.platformForgotPassword(email);
				forgotPasswordSent = true;
			} else {
				const result = isSignUp
					? await api.signUp(email, password)
					: await api.signIn(email, password);
				user.set({ token: result.access_token, email });
				await redirectAfterLogin();
			}
		} catch (err) {
			const msg = err instanceof Error ? err.message : 'Authentication failed';
			if (msg.toLowerCase().includes('waitlist')) {
				waitlisted = true;
				error = '';
			} else {
				error = parseError(msg);
			}
		} finally {
			submitting = false;
		}
	}
</script>

<svelte:head>
	<title>{isForgotPassword ? 'Reset Password' : isSignUp ? 'Sign Up' : 'Sign In'} - Eurobase Console</title>
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
					{isForgotPassword ? 'Reset your password' : isSignUp ? 'Create your account' : 'Sign in to your account'}
				</h2>
				<p class="mt-1 text-sm text-gray-500">
					{isForgotPassword ? 'Enter your email to receive a reset link' : isSignUp ? 'Get started with Eurobase' : 'Access your Eurobase console'}
				</p>

				{#if waitlisted}
					<div class="mt-4 rounded-lg bg-eurobase-50 border border-eurobase-200 p-4 text-sm text-eurobase-800">
						<div class="flex items-center gap-2 font-medium">
							<svg class="h-5 w-5 text-eurobase-600 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" d="M12 6v6h4.5m4.5 0a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" />
							</svg>
							You're on the waitlist
						</div>
						<p class="mt-2 text-eurobase-700">Eurobase is currently in closed beta. We've noted your interest and will notify you at <strong>{email}</strong> when your spot opens up.</p>
						<button onclick={() => { waitlisted = false; isSignUp = false; email = ''; password = ''; confirmPassword = ''; }} class="mt-3 text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer underline">Already have an account? Sign in</button>
					</div>
				{:else if error}
					<div class="mt-4 flex items-start gap-2.5 rounded-lg bg-red-50 border border-red-200 p-3">
						<svg class="h-4 w-4 mt-0.5 shrink-0 text-red-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
						</svg>
						<p class="text-sm text-red-700">{error}</p>
					</div>
				{/if}

				{#if isForgotPassword && forgotPasswordSent}
					<div class="mt-4 rounded-lg bg-emerald-50 border border-emerald-200 p-4 text-sm text-emerald-700">
						<p class="font-medium">Check your inbox</p>
						<p class="mt-1">If an account exists for <strong>{email}</strong>, we've sent a password reset link.</p>
					</div>
					<div class="mt-4 text-center">
						<button onclick={() => { isForgotPassword = false; forgotPasswordSent = false; error = ''; }} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">Back to sign in</button>
					</div>
				{:else}
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

						{#if !isForgotPassword}
							<div>
								<label for="password" class="block text-sm font-medium text-gray-700">Password</label>
								<div class="relative mt-1">
									<input
										id="password"
										type={showPassword ? 'text' : 'password'}
										bind:value={password}
										required
										minlength="8"
										placeholder={isSignUp ? 'At least 8 characters' : ''}
										class="block w-full rounded-lg border border-gray-300 px-3.5 py-2.5 pr-10 text-sm text-gray-900 shadow-sm placeholder:text-gray-400 focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
									/>
									<button
										type="button"
										onclick={() => showPassword = !showPassword}
										class="absolute inset-y-0 right-0 flex items-center pr-3 text-gray-400 hover:text-gray-600 cursor-pointer"
										tabindex="-1"
									>
										{#if showPassword}
											<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M3.98 8.223A10.477 10.477 0 0 0 1.934 12C3.226 16.338 7.244 19.5 12 19.5c.993 0 1.953-.138 2.863-.395M6.228 6.228A10.451 10.451 0 0 1 12 4.5c4.756 0 8.773 3.162 10.065 7.498a10.522 10.522 0 0 1-4.293 5.774M6.228 6.228 3 3m3.228 3.228 3.65 3.65m7.894 7.894L21 21m-3.228-3.228-3.65-3.65m0 0a3 3 0 1 0-4.243-4.243m4.242 4.242L9.88 9.88" /></svg>
										{:else}
											<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M2.036 12.322a1.012 1.012 0 0 1 0-.639C3.423 7.51 7.36 4.5 12 4.5c4.638 0 8.573 3.007 9.963 7.178.07.207.07.431 0 .639C20.577 16.49 16.64 19.5 12 19.5c-4.638 0-8.573-3.007-9.963-7.178Z" /><path stroke-linecap="round" stroke-linejoin="round" d="M15 12a3 3 0 1 1-6 0 3 3 0 0 1 6 0Z" /></svg>
										{/if}
									</button>
								</div>
							</div>
							{#if isSignUp}
								<div>
									<label for="confirm-password" class="block text-sm font-medium text-gray-700">Confirm Password</label>
									<input
										id="confirm-password"
										type={showPassword ? 'text' : 'password'}
										bind:value={confirmPassword}
										required
										minlength="8"
										placeholder="Repeat your password"
										class="mt-1 block w-full rounded-lg border border-gray-300 px-3.5 py-2.5 text-sm text-gray-900 shadow-sm placeholder:text-gray-400 focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
									/>
								</div>
							{/if}
						{/if}

						{#if !isSignUp && !isForgotPassword}
							<div class="text-right">
								<button type="button" onclick={() => { isForgotPassword = true; error = ''; }} class="text-xs text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">Forgot password?</button>
							</div>
						{/if}

						<button
							type="submit"
							disabled={submitting}
							class="w-full rounded-lg bg-eurobase-600 px-4 py-2.5 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
						>
							{#if submitting}
								{isForgotPassword ? 'Sending...' : isSignUp ? 'Creating account...' : 'Signing in...'}
							{:else}
								{isForgotPassword ? 'Send Reset Link' : isSignUp ? 'Create Account' : 'Sign In'}
							{/if}
						</button>
					</form>

					<div class="mt-4 text-center text-sm text-gray-500">
						{#if isForgotPassword}
							<button onclick={() => { isForgotPassword = false; error = ''; }} class="text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">Back to sign in</button>
						{:else if isSignUp}
							Already have an account?
							<button onclick={() => { isSignUp = false; error = ''; confirmPassword = ''; }} class="text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">Sign in</button>
						{:else}
							Don't have an account?
							<button onclick={() => { isSignUp = true; error = ''; }} class="text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">Sign up</button>
						{/if}
					</div>
				{/if}
			</div>

			<p class="mt-6 text-center text-xs text-gray-400">
				All data stored exclusively in EU datacenters under EU law.
				<br />
				No US CLOUD Act exposure. GDPR compliant by design.
			</p>
		</div>
	</div>
</div>
