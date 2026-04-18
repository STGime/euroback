<script lang="ts">
	import { goto } from '$app/navigation';
	import { onMount } from 'svelte';
	import { api } from '$lib/api.js';

	let { children } = $props();
	let ready = $state(false);

	onMount(async () => {
		try {
			const profile = await api.getProfile();
			if (!profile.is_superadmin) {
				goto('/projects');
				return;
			}
			ready = true;
		} catch {
			goto('/login');
		}
	});
</script>

{#if ready}
	{@render children()}
{:else}
	<div class="p-6 text-gray-400 text-sm">Checking permissions…</div>
{/if}
