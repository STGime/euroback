<script lang="ts">
	let { projectId, active }: { projectId: string; active: 'requests' | 'sql' } = $props();

	const items = [
		{ id: 'requests', label: 'Requests', href: '' },
		{ id: 'sql', label: 'SQL history', href: '/sql' }
	] as const;
</script>

<nav class="mb-6 flex gap-6 border-b border-gray-200" aria-label="Log views">
	{#each items as item}
		<a
			href="/p/{projectId}/logs{item.href}"
			class="-mb-px border-b-2 pb-3 text-sm font-medium transition-colors {active === item.id
				? 'border-eurobase-600 text-eurobase-700'
				: 'border-transparent text-gray-500 hover:border-gray-300 hover:text-gray-700'}"
			aria-current={active === item.id ? 'page' : undefined}
		>
			{item.label}
		</a>
	{/each}
</nav>
