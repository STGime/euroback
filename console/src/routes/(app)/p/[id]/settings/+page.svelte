<script lang="ts">
	import { getContext } from 'svelte';
	import type { Project } from '$lib/api.js';

	const projectCtx = getContext<{ id: string; project: Project | null }>('projectId');
</script>

<div class="mx-auto max-w-4xl space-y-6">
	{#if projectCtx.project}
		<!-- Project Info -->
		<div class="rounded-xl border border-gray-200 bg-white p-6">
			<h2 class="text-lg font-semibold text-gray-900">Project Settings</h2>
			<div class="mt-4 grid grid-cols-1 gap-4 sm:grid-cols-2">
				<div>
					<label class="block text-sm font-medium text-gray-500">Project Name</label>
					<p class="mt-1 text-sm text-gray-900">{projectCtx.project.name}</p>
				</div>
				<div>
					<label class="block text-sm font-medium text-gray-500">Slug</label>
					<p class="mt-1 font-mono text-sm text-gray-900">{projectCtx.project.slug}</p>
				</div>
				<div>
					<label class="block text-sm font-medium text-gray-500">Region</label>
					<p class="mt-1 text-sm text-gray-900">{projectCtx.project.region}</p>
				</div>
				<div>
					<label class="block text-sm font-medium text-gray-500">Plan</label>
					<p class="mt-1 text-sm text-gray-900 capitalize">{projectCtx.project.plan}</p>
				</div>
			</div>
		</div>

		<!-- API Keys placeholder -->
		<div class="rounded-xl border border-gray-200 bg-white p-6">
			<h2 class="text-lg font-semibold text-gray-900">API Keys</h2>
			<p class="mt-1 text-sm text-gray-500">Manage your project's API keys for client and server access.</p>
			<p class="mt-4 text-xs text-gray-400">API key management coming in Phase 3</p>
		</div>

		<!-- Danger Zone -->
		<div class="rounded-xl border border-red-200 bg-white p-6">
			<h2 class="text-lg font-semibold text-red-600">Danger Zone</h2>
			<p class="mt-1 text-sm text-gray-500">Irreversible actions for this project.</p>
			<div class="mt-4">
				<button
					disabled
					class="rounded-lg border border-red-300 px-4 py-2 text-sm font-medium text-red-600 opacity-50 cursor-not-allowed"
				>
					Delete Project
				</button>
			</div>
		</div>
	{/if}
</div>
