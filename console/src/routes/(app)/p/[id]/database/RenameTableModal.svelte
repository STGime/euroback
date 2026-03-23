<script lang="ts">
	import { api } from '$lib/api.js';

	interface Props {
		open: boolean;
		tableName: string;
		projectId: string;
		onClose: () => void;
		onRenamed: (newName: string) => void;
	}

	let { open, tableName, projectId, onClose, onRenamed }: Props = $props();

	let newName = $state('');
	let saving = $state(false);
	let error: string | null = $state(null);

	$effect(() => {
		if (open) {
			newName = tableName;
			error = null;
		}
	});

	let hasChanges = $derived(newName.trim() !== '' && newName !== tableName);

	async function handleRename() {
		if (!hasChanges) return;
		saving = true;
		error = null;
		try {
			await api.renameTable(projectId, tableName, newName.trim());
			onRenamed(newName.trim());
			onClose();
		} catch (err) {
			const raw = err instanceof Error ? err.message : String(err);
			const jsonMatch = raw.match(/\{"error":"(.+?)"\}/);
			error = jsonMatch ? jsonMatch[1] : raw;
		} finally {
			saving = false;
		}
	}
</script>

{#if open}
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<button
			type="button"
			class="fixed inset-0 bg-black/50 cursor-default"
			onclick={onClose}
			tabindex="-1"
			aria-label="Close modal"
		></button>
		<div class="relative z-10 w-full max-w-sm rounded-xl bg-white shadow-2xl">
			<div class="flex items-center justify-between border-b border-gray-200 px-6 py-4">
				<h2 class="text-lg font-semibold text-gray-900">Rename Table</h2>
				<button
					type="button"
					class="cursor-pointer rounded-lg p-1.5 text-gray-400 hover:bg-gray-100 hover:text-gray-600"
					onclick={onClose}
					aria-label="Close"
				>
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12" />
					</svg>
				</button>
			</div>

			<div class="px-6 py-5 space-y-4">
				{#if error}
					<div class="flex items-start gap-2 rounded-lg border border-red-200 bg-red-50 px-4 py-3">
						<svg class="h-4 w-4 mt-0.5 shrink-0 text-red-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
						</svg>
						<p class="text-sm text-red-700">{error}</p>
					</div>
				{/if}

				<div>
					<label class="block text-sm font-medium text-gray-700 mb-1" for="table-name">Table name</label>
					<input
						id="table-name"
						type="text"
						bind:value={newName}
						class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono text-gray-900 focus:border-eurobase-500 focus:ring-1 focus:ring-eurobase-500/20 focus:outline-none"
						onkeydown={(e) => { if (e.key === 'Enter' && hasChanges) handleRename(); }}
					/>
					<p class="mt-1 text-xs text-gray-400">Letters, digits, and underscores only. Max 63 characters.</p>
				</div>
			</div>

			<div class="flex items-center justify-end gap-3 border-t border-gray-200 px-6 py-4">
				<button
					type="button"
					class="cursor-pointer rounded-lg border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors"
					onclick={onClose}
				>
					Cancel
				</button>
				<button
					type="button"
					class="cursor-pointer rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
					disabled={!hasChanges || saving}
					onclick={handleRename}
				>
					{saving ? 'Renaming...' : 'Rename'}
				</button>
			</div>
		</div>
	</div>
{/if}
