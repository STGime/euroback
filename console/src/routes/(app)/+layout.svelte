<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { onMount } from 'svelte';
	import { user, logout } from '$lib/stores.js';
	import { api } from '$lib/api.js';
	import { PUBLIC_BUILD_SHA } from '$env/static/public';

	let { children } = $props();
	let displayName = $state<string | null>(null);
	let isSuperadmin = $state<boolean>(false);

	onMount(async () => {
		if (!$user) {
			goto('/login');
			return;
		}
		try {
			const profile = await api.getProfile();
			displayName = profile.display_name;
			isSuperadmin = profile.is_superadmin === true;
		} catch {
			// Silently ignore — falls back to email display.
		}
	});

	let navItems = $derived(
		[
			{ label: 'Projects', href: '/projects', icon: 'projects' },
			{ label: 'Account', href: '/account', icon: 'account' },
			{ label: 'Documentation', href: '/docs', icon: 'docs' },
			...(isSuperadmin ? [{ label: 'Admin', href: '/admin', icon: 'admin' }] : [])
		]
	);

	let currentPath = $derived(page.url.pathname);

	async function handleLogout() {
		logout();
		goto('/login');
	}
</script>

<div class="flex min-h-screen bg-gray-50">
	<!-- Sidebar -->
	<aside class="hidden md:flex md:w-64 md:flex-col border-r border-gray-200 bg-white">
		<div class="flex h-full flex-col">
			<!-- Logo -->
			<div class="flex h-16 items-center gap-2.5 border-b border-gray-200 px-6">
				<div class="flex h-8 w-8 items-center justify-center rounded-lg bg-eurobase-600">
					<svg class="h-5 w-5 text-white" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M9 12.75 11.25 15 15 9.75m-3-7.036A11.959 11.959 0 0 1 3.598 6 11.99 11.99 0 0 0 3 9.749c0 5.592 3.824 10.29 9 11.623 5.176-1.332 9-6.03 9-11.622 0-1.31-.21-2.571-.598-3.751h-.152c-3.196 0-6.1-1.248-8.25-3.285Z" />
					</svg>
				</div>
				<span class="text-lg font-bold text-gray-900">Eurobase</span>
			</div>

			<!-- Navigation -->
			<nav class="flex-1 px-3 py-4 space-y-1">
				{#each navItems as item}
					<a
						href={item.href}
						class="flex items-center gap-3 rounded-lg px-3 py-2.5 text-sm font-medium transition-colors {currentPath === item.href ? 'bg-eurobase-50 text-eurobase-700' : 'text-gray-600 hover:bg-gray-50 hover:text-gray-900'}"
					>
						{#if item.icon === 'projects'}
							<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" d="M3.75 6A2.25 2.25 0 0 1 6 3.75h2.25A2.25 2.25 0 0 1 10.5 6v2.25a2.25 2.25 0 0 1-2.25 2.25H6a2.25 2.25 0 0 1-2.25-2.25V6ZM3.75 15.75A2.25 2.25 0 0 1 6 13.5h2.25a2.25 2.25 0 0 1 2.25 2.25V18a2.25 2.25 0 0 1-2.25 2.25H6A2.25 2.25 0 0 1 3.75 18v-2.25ZM13.5 6a2.25 2.25 0 0 1 2.25-2.25H18A2.25 2.25 0 0 1 20.25 6v2.25A2.25 2.25 0 0 1 18 10.5h-2.25a2.25 2.25 0 0 1-2.25-2.25V6ZM13.5 15.75a2.25 2.25 0 0 1 2.25-2.25H18a2.25 2.25 0 0 1 2.25 2.25V18A2.25 2.25 0 0 1 18 20.25h-2.25a2.25 2.25 0 0 1-2.25-2.25v-2.25Z" />
							</svg>
						{:else if item.icon === 'account'}
							<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" d="M15.75 6a3.75 3.75 0 1 1-7.5 0 3.75 3.75 0 0 1 7.5 0ZM4.501 20.118a7.5 7.5 0 0 1 14.998 0A17.933 17.933 0 0 1 12 21.75c-2.676 0-5.216-.584-7.499-1.632Z" />
							</svg>
						{:else if item.icon === 'docs'}
							<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" d="M12 6.042A8.967 8.967 0 0 0 6 3.75c-1.052 0-2.062.18-3 .512v14.25A8.987 8.987 0 0 1 6 18c2.305 0 4.408.867 6 2.292m0-14.25a8.966 8.966 0 0 1 6-2.292c1.052 0 2.062.18 3 .512v14.25A8.987 8.987 0 0 0 18 18a8.967 8.967 0 0 0-6 2.292m0-14.25v14.25" />
							</svg>
						{:else if item.icon === 'admin'}
							<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" d="M9.594 3.94c.09-.542.56-.94 1.11-.94h2.593c.55 0 1.02.398 1.11.94l.213 1.281c.063.374.313.686.645.87.074.04.147.083.22.127.325.196.72.257 1.075.124l1.217-.456a1.125 1.125 0 0 1 1.37.49l1.296 2.247a1.125 1.125 0 0 1-.26 1.431l-1.003.827c-.293.241-.438.613-.43.992a7.723 7.723 0 0 1 0 .255c-.008.378.137.75.43.991l1.004.827c.424.35.534.955.26 1.43l-1.298 2.247a1.125 1.125 0 0 1-1.369.491l-1.217-.456c-.355-.133-.75-.072-1.076.124a6.47 6.47 0 0 1-.22.128c-.331.183-.581.495-.644.869l-.213 1.281c-.09.543-.56.94-1.11.94h-2.594c-.55 0-1.019-.398-1.11-.94l-.213-1.281c-.062-.374-.312-.686-.644-.87a6.52 6.52 0 0 1-.22-.127c-.325-.196-.72-.257-1.076-.124l-1.217.456a1.125 1.125 0 0 1-1.369-.49l-1.297-2.247a1.125 1.125 0 0 1 .26-1.431l1.004-.827c.292-.24.437-.613.43-.991a6.932 6.932 0 0 1 0-.255c.007-.38-.138-.751-.43-.992l-1.004-.827a1.125 1.125 0 0 1-.26-1.43l1.297-2.247a1.125 1.125 0 0 1 1.37-.491l1.216.456c.356.133.751.072 1.076-.124.072-.044.146-.086.22-.128.332-.183.582-.495.644-.869l.214-1.28Z" />
								<path stroke-linecap="round" stroke-linejoin="round" d="M15 12a3 3 0 1 1-6 0 3 3 0 0 1 6 0Z" />
							</svg>
						{/if}
						{item.label}
					</a>
				{/each}
			</nav>

			<!-- Sidebar footer -->
			<div class="border-t border-gray-200 p-4 space-y-3">
				<button
					onclick={handleLogout}
					class="flex w-full items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium text-gray-600 hover:bg-gray-50 hover:text-gray-900 transition-colors cursor-pointer"
				>
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M15.75 9V5.25A2.25 2.25 0 0 0 13.5 3h-6a2.25 2.25 0 0 0-2.25 2.25v13.5A2.25 2.25 0 0 0 7.5 21h6a2.25 2.25 0 0 0 2.25-2.25V15m3 0 3-3m0 0-3-3m3 3H9" />
					</svg>
					Sign Out
				</button>
				<div class="flex items-center gap-2 text-xs text-gray-400">
					<svg class="h-3.5 w-3.5" viewBox="0 0 24 24" fill="currentColor">
						<circle cx="12" cy="12" r="10" fill="none" stroke="currentColor" stroke-width="1.5"/>
						<circle cx="12" cy="5" r="0.8" />
						<circle cx="15.5" cy="6.3" r="0.8" />
						<circle cx="17.7" cy="9.5" r="0.8" />
						<circle cx="17.7" cy="14.5" r="0.8" />
						<circle cx="15.5" cy="17.7" r="0.8" />
						<circle cx="12" cy="19" r="0.8" />
						<circle cx="8.5" cy="17.7" r="0.8" />
						<circle cx="6.3" cy="14.5" r="0.8" />
						<circle cx="6.3" cy="9.5" r="0.8" />
						<circle cx="8.5" cy="6.3" r="0.8" />
					</svg>
					<span>EU-Sovereign Infrastructure</span>
				</div>
				{#if PUBLIC_BUILD_SHA}
					<div class="text-[10px] text-gray-300 font-mono">{PUBLIC_BUILD_SHA.slice(0, 7)}</div>
				{/if}
			</div>
		</div>
	</aside>

	<!-- Main content -->
	<div class="flex flex-1 flex-col min-w-0">
		<!-- Top bar -->
		<header class="flex h-16 items-center justify-between border-b border-gray-200 bg-white px-6">
			<!-- Mobile menu button -->
			<button aria-label="Open menu" class="md:hidden rounded-lg p-2 text-gray-500 hover:bg-gray-100 cursor-pointer">
				<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" d="M3.75 6.75h16.5M3.75 12h16.5m-16.5 5.25h16.5" />
				</svg>
			</button>

			<div class="md:hidden flex items-center gap-2">
				<div class="flex h-7 w-7 items-center justify-center rounded-md bg-eurobase-600">
					<svg class="h-4 w-4 text-white" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M9 12.75 11.25 15 15 9.75m-3-7.036A11.959 11.959 0 0 1 3.598 6 11.99 11.99 0 0 0 3 9.749c0 5.592 3.824 10.29 9 11.623 5.176-1.332 9-6.03 9-11.622 0-1.31-.21-2.571-.598-3.751h-.152c-3.196 0-6.1-1.248-8.25-3.285Z" />
					</svg>
				</div>
				<span class="font-semibold text-gray-900">Eurobase</span>
			</div>

			<!-- Spacer for desktop -->
			<div class="hidden md:block"></div>

			<!-- User menu -->
			<div class="flex items-center gap-3">
				<span class="text-sm text-gray-500 hidden sm:block">{displayName ?? $user?.email ?? ''}</span>
				<div class="flex h-8 w-8 items-center justify-center rounded-full bg-eurobase-100 text-sm font-medium text-eurobase-700">
					{(displayName ?? $user?.email ?? '?')[0].toUpperCase()}
				</div>
			</div>
		</header>

		<!-- Page content -->
		<main class="flex-1 p-6 lg:p-8 min-w-0 overflow-hidden">
			{@render children()}
		</main>
	</div>
</div>
