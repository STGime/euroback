<script lang="ts">
	// Destructive-action confirm modal, mirrors the pattern from
	// p/[id]/settings/+page.svelte:330-383. Requires typing the
	// project name to enable the confirm button.

	let {
		projectName,
		description,
		open = $bindable(false),
		busy = false,
		error = null,
		onConfirm = () => {},
	}: {
		projectName: string;
		description: string;
		open?: boolean;
		busy?: boolean;
		error?: string | null;
		onConfirm?: () => void;
	} = $props();

	let typed = $state('');
	let matches = $derived(typed.trim() === projectName);

	function close() {
		open = false;
		typed = '';
	}
</script>

{#if open}
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<button
			type="button"
			class="fixed inset-0 bg-black/50 cursor-default"
			onclick={close}
			tabindex="-1"
			aria-label="Close"
		></button>
		<div class="relative z-10 w-full max-w-md rounded-xl bg-white shadow-2xl p-6">
			<div class="flex items-center gap-3 mb-4">
				<div class="flex h-10 w-10 items-center justify-center rounded-full bg-red-100">
					<svg
						class="h-5 w-5 text-red-600"
						fill="none"
						viewBox="0 0 24 24"
						stroke-width="1.5"
						stroke="currentColor"
					>
						<path
							stroke-linecap="round"
							stroke-linejoin="round"
							d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z"
						/>
					</svg>
				</div>
				<div>
					<h3 class="text-sm font-semibold text-gray-900">Restore project</h3>
					<p class="text-xs text-gray-500">
						Your project will be unavailable during the ~2–5 minute cutover.
					</p>
				</div>
			</div>

			{#if error}
				<div class="mb-4 flex items-start gap-2 rounded-lg border border-red-200 bg-red-50 px-3 py-2">
					<p class="text-sm text-red-700">{error}</p>
				</div>
			{/if}

			<p class="text-sm text-gray-600 mb-2">{description}</p>
			<p class="text-xs text-gray-500 mb-4">
				A new managed-PG instance will be provisioned. The old instance is retained for 7 days so
				this operation is reversible.
			</p>

			<div class="mb-5">
				<label for="confirm-name" class="block text-sm font-medium text-gray-700 mb-1">
					Type <strong>{projectName}</strong> to confirm
				</label>
				<input
					id="confirm-name"
					type="text"
					bind:value={typed}
					placeholder={projectName}
					class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 placeholder-gray-300 focus:border-red-500 focus:outline-none"
				/>
			</div>

			<div class="flex justify-end gap-3">
				<button
					type="button"
					class="cursor-pointer rounded-lg border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors"
					onclick={close}
				>
					Cancel
				</button>
				<button
					type="button"
					class="cursor-pointer rounded-lg bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
					disabled={!matches || busy}
					onclick={onConfirm}
				>
					{busy ? 'Starting…' : 'Restore'}
				</button>
			</div>
		</div>
	</div>
{/if}
