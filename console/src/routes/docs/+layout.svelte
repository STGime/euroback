<script lang="ts">
	import { page } from '$app/state';
	import { user } from '$lib/stores';
	import { chapters } from '$lib/docs/registry';

	let { children } = $props();
	let tocOpen = $state(false);

	// Active chapter from the URL: /docs → welcome, /docs/<id> → id.
	let activeId = $derived(page.url.pathname.replace(/^\/docs\/?/, '') || 'welcome');
	function hrefFor(id: string): string {
		return id === 'welcome' ? '/docs' : `/docs/${id}`;
	}
</script>

<div class="min-h-screen bg-gray-50">
	<!-- Public header. The prerendered HTML is the anonymous variant (the
	     user store is null on the server); after hydration a signed-in
	     reader gets a single "Go to console" link instead of the
	     sign-in / sign-up pair. -->
	<header class="border-b border-gray-200 bg-white">
		<div class="mx-auto flex max-w-7xl items-center justify-between px-6 py-4">
			<div class="flex items-center gap-6">
				<a href="https://eurobase.app" class="text-lg font-bold text-gray-900">Eurobase</a>
				<nav class="hidden sm:flex items-center gap-4 text-sm">
					<a href="/docs" class="font-medium text-eurobase-700">Docs</a>
					<a href="/pricing" class="text-gray-600 hover:text-gray-900">Pricing</a>
					<a href="https://eurobase.app/faq" class="text-gray-600 hover:text-gray-900">FAQ</a>
				</nav>
			</div>
			<div class="flex items-center gap-3 text-sm">
				{#if $user}
					<a href="/projects" class="rounded-lg bg-eurobase-600 px-4 py-2 font-semibold text-white shadow-sm hover:bg-eurobase-700 transition-colors">Go to console</a>
				{:else}
					<a href="/login" class="text-gray-600 hover:text-gray-900">Sign in</a>
					<a href="/login?signup=1" class="rounded-lg bg-eurobase-600 px-4 py-2 font-semibold text-white shadow-sm hover:bg-eurobase-700 transition-colors">Get started</a>
				{/if}
			</div>
		</div>
	</header>

	<div class="mx-auto flex max-w-7xl gap-8 px-6 py-8">
		<!-- Desktop TOC: real links, one URL per chapter -->
		<nav class="hidden lg:block w-56 shrink-0" aria-label="Documentation chapters">
			<div class="sticky top-8">
				<h3 class="text-xs font-semibold uppercase tracking-wider text-gray-400 mb-3">Contents</h3>
				<ul class="space-y-0.5">
					{#each chapters as ch}
						<li>
							<a
								href={hrefFor(ch.id)}
								aria-current={activeId === ch.id ? 'page' : undefined}
								class="block text-sm py-1 pl-3 border-l-2 transition-colors {activeId === ch.id ? 'text-eurobase-700 font-medium border-eurobase-600' : 'text-gray-500 border-transparent hover:text-gray-700 hover:border-gray-300'}"
							>
								{ch.label}
							</a>
						</li>
					{/each}
				</ul>
			</div>
		</nav>

		<!-- Mobile TOC dropdown -->
		<div class="lg:hidden fixed top-20 right-4 z-30">
			<button
				type="button"
				onclick={() => (tocOpen = !tocOpen)}
				class="flex items-center gap-1.5 rounded-lg bg-white border border-gray-200 px-3 py-2 text-xs font-medium text-gray-700 shadow-sm cursor-pointer"
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" d="M3.75 6.75h16.5M3.75 12h16.5m-16.5 5.25h16.5" />
				</svg>
				Contents
			</button>
			{#if tocOpen}
				<!-- svelte-ignore a11y_no_static_element_interactions -->
				<div class="fixed inset-0 z-40" onclick={() => (tocOpen = false)} onkeydown={(e) => e.key === 'Escape' && (tocOpen = false)}></div>
				<div class="absolute right-0 mt-1 w-64 rounded-xl bg-white border border-gray-200 shadow-lg z-50 py-2 max-h-[70vh] overflow-y-auto">
					{#each chapters as ch}
						<a
							href={hrefFor(ch.id)}
							onclick={() => (tocOpen = false)}
							class="block px-4 py-1.5 text-sm {activeId === ch.id ? 'text-eurobase-700 font-medium bg-eurobase-50' : 'text-gray-600 hover:bg-gray-50'}"
						>
							{ch.label}
						</a>
					{/each}
				</div>
			{/if}
		</div>

		<!-- Content -->
		<main class="flex-1 min-w-0 max-w-3xl pb-24">
			{@render children()}
		</main>
	</div>
</div>
