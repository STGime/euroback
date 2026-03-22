<script lang="ts">
	import { api } from '$lib/api.js';
	import { formatBytes } from '$lib/utils.js';

	interface Props {
		projectId: string;
		currentPrefix: string;
		onuploaded?: () => void;
	}

	let { projectId, currentPrefix, onuploaded }: Props = $props();

	interface UploadItem {
		file: File;
		progress: number;
		status: 'pending' | 'uploading' | 'done' | 'error';
		error?: string;
	}

	let uploads = $state<UploadItem[]>([]);
	let dragOver = $state(false);
	let fileInput = $state<HTMLInputElement | null>(null);

	let hasActive = $derived(uploads.some((u) => u.status === 'uploading' || u.status === 'pending'));

	function handleDragOver(e: DragEvent) {
		e.preventDefault();
		dragOver = true;
	}

	function handleDragLeave() {
		dragOver = false;
	}

	function handleDrop(e: DragEvent) {
		e.preventDefault();
		dragOver = false;
		if (e.dataTransfer?.files) {
			addFiles(Array.from(e.dataTransfer.files));
		}
	}

	function handleFileSelect(e: Event) {
		const input = e.target as HTMLInputElement;
		if (input.files) {
			addFiles(Array.from(input.files));
			input.value = '';
		}
	}

	function updateUpload(file: File, patch: Partial<UploadItem>) {
		uploads = uploads.map((u) => (u.file === file ? { ...u, ...patch } : u));
	}

	function addFiles(files: File[]) {
		const newItems: UploadItem[] = files.map((file) => ({
			file,
			progress: 0,
			status: 'pending'
		}));
		uploads = [...uploads, ...newItems];

		for (const item of newItems) {
			uploadOne(item.file);
		}
	}

	async function uploadOne(file: File) {
		updateUpload(file, { status: 'uploading', progress: 10 });

		const key = currentPrefix ? `${currentPrefix}${file.name}` : file.name;

		try {
			updateUpload(file, { progress: 30 });

			await api.uploadFile(projectId, file, key);

			updateUpload(file, { progress: 100, status: 'done' });
		} catch (err) {
			updateUpload(file, {
				status: 'error',
				error: err instanceof Error ? err.message : 'Upload failed'
			});
		}

		// If all uploads are done, notify parent
		if (uploads.every((u) => u.status === 'done' || u.status === 'error')) {
			const hadSuccess = uploads.some((u) => u.status === 'done');
			if (hadSuccess && onuploaded) {
				setTimeout(() => {
					onuploaded?.();
				}, 500);
			}
		}
	}

	function clearCompleted() {
		uploads = uploads.filter((u) => u.status !== 'done');
	}
</script>

<div
	role="button"
	tabindex="0"
	class="relative rounded-xl border-2 border-dashed transition-colors {dragOver
		? 'border-eurobase-500 bg-eurobase-50'
		: 'border-gray-300 bg-gray-50 hover:border-gray-400'}"
	ondragover={handleDragOver}
	ondragleave={handleDragLeave}
	ondrop={handleDrop}
	onclick={() => fileInput?.click()}
	onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') fileInput?.click(); }}
>
	<input
		bind:this={fileInput}
		type="file"
		multiple
		class="hidden"
		onchange={handleFileSelect}
	/>

	{#if uploads.length === 0}
		<div class="flex flex-col items-center py-8 px-4">
			<svg class="h-10 w-10 text-gray-400" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" d="M3 16.5v2.25A2.25 2.25 0 0 0 5.25 21h13.5A2.25 2.25 0 0 0 21 18.75V16.5m-13.5-9L12 3m0 0 4.5 4.5M12 3v13.5" />
			</svg>
			<p class="mt-2 text-sm font-medium text-gray-700">
				{#if dragOver}Drop files here{:else}Drag and drop files, or click to browse{/if}
			</p>
			<p class="mt-1 text-xs text-gray-500">Upload files to your project storage</p>
		</div>
	{:else}
		<!-- Upload progress list -->
		<!-- svelte-ignore a11y_click_events_have_key_events -->
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div class="p-4 space-y-3" onclick={(e) => e.stopPropagation()}>
			{#each uploads as item}
				<div class="flex items-center gap-3">
					<div class="flex-1 min-w-0">
						<div class="flex items-center justify-between">
							<p class="text-sm font-medium text-gray-700 truncate">{item.file.name}</p>
							<span class="text-xs text-gray-500 ml-2 shrink-0">{formatBytes(item.file.size)}</span>
						</div>
						<div class="mt-1 h-1.5 w-full rounded-full bg-gray-200 overflow-hidden">
							<div
								class="h-full rounded-full transition-all duration-300 {item.status === 'error'
									? 'bg-red-500'
									: item.status === 'done'
										? 'bg-emerald-500'
										: 'bg-eurobase-600'}"
								style="width: {item.progress}%"
							></div>
						</div>
						{#if item.status === 'error'}
							<p class="mt-0.5 text-xs text-red-600">{item.error}</p>
						{/if}
					</div>
					{#if item.status === 'done'}
						<svg class="h-5 w-5 text-emerald-500 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" />
						</svg>
					{:else if item.status === 'error'}
						<svg class="h-5 w-5 text-red-500 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12" />
						</svg>
					{:else}
						<svg class="h-5 w-5 text-eurobase-600 animate-spin shrink-0" fill="none" viewBox="0 0 24 24">
							<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
							<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
						</svg>
					{/if}
				</div>
			{/each}

			{#if !hasActive && uploads.length > 0}
				<button
					type="button"
					onclick={() => clearCompleted()}
					class="text-xs text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer"
				>
					Clear completed
				</button>
			{/if}
		</div>
	{/if}
</div>
