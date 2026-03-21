<script lang="ts">
	import { onMount, getContext } from 'svelte';
	import { page } from '$app/stores';
	import { api, type FileInfo } from '$lib/api.js';
	import { formatBytes, formatRelativeTime, getFileIcon, inferContentType } from '$lib/utils.js';
	import FileUploader from '$lib/components/FileUploader.svelte';

	// ---- State ----

	let files = $state<FileInfo[]>([]);
	let loading = $state(true);
	let error = $state('');
	let viewMode = $state<'list' | 'grid'>('list');
	let searchQuery = $state('');
	let currentPrefix = $state('');
	let selectedFile = $state<FileInfo | null>(null);
	let showNewFolderModal = $state(false);
	let newFolderName = $state('');

	// Signed URL state
	let signedUrl = $state('');
	let signedUrlExpiry = $state('');
	let signedUrlLoading = $state(false);
	let selectedExpiry = $state(3600);

	// Preview state
	let previewUrl = $state<string | null>(null);
	let previewText = $state<string | null>(null);
	let previewLoading = $state(false);

	// Actions dropdown
	let openDropdown = $state<string | null>(null);

	// Project slug from layout context
	const projectCtx = getContext<{ id: string; project: import('$lib/api.js').Project | null }>('projectId');
	let projectSlug = $derived(projectCtx.project?.slug ?? $page.params.id);

	// Breadcrumb segments
	let breadcrumbs = $derived.by(() => {
		if (!currentPrefix) return [];
		const parts = currentPrefix.split('/').filter(Boolean);
		return parts.map((part, i) => ({
			label: part,
			prefix: parts.slice(0, i + 1).join('/') + '/'
		}));
	});

	// Filtered files: separate folders and files, apply search
	let filteredItems = $derived.by(() => {
		let items = files;
		if (searchQuery.trim()) {
			const q = searchQuery.toLowerCase();
			items = items.filter((f) => {
				const name = f.key.replace(currentPrefix, '');
				return name.toLowerCase().includes(q);
			});
		}
		return items;
	});

	let folders = $derived(
		filteredItems
			.filter((f) => f.key.endsWith('/'))
			.map((f) => ({ ...f, displayName: f.key.replace(currentPrefix, '').replace(/\/$/, '') }))
	);

	let regularFiles = $derived(
		filteredItems
			.filter((f) => !f.key.endsWith('/'))
			.map((f) => ({ ...f, displayName: f.key.replace(currentPrefix, '') }))
	);

	// ---- Lifecycle ----

	let lastSlug = '';
	$effect(() => {
		// Reload files when slug becomes available or changes
		if (projectSlug && projectSlug !== $page.params.id && projectSlug !== lastSlug) {
			lastSlug = projectSlug;
			loadFiles();
		}
	});

	// ---- Actions ----

	async function loadFiles() {
		loading = true;
		error = '';
		try {
			const res = await api.listFiles(projectSlug, { prefix: currentPrefix || undefined });
			files = res.objects;
		} catch (err_) {
			error = err_ instanceof Error ? err_.message : 'Failed to load files';
			files = [];
		} finally {
			loading = false;
		}
	}

	function navigateToFolder(prefix: string) {
		currentPrefix = prefix;
		selectedFile = null;
		signedUrl = '';
		loadFiles();
	}

	function navigateToRoot() {
		navigateToFolder('');
	}

	function selectFile(file: FileInfo) {
		selectedFile = file;
		signedUrl = '';
		signedUrlExpiry = '';
		loadPreview(file);
	}

	async function loadPreview(file: FileInfo) {
		// Clean up previous preview
		if (previewUrl) { URL.revokeObjectURL(previewUrl); previewUrl = null; }
		previewText = null;
		previewLoading = true;

		try {
			const ct = inferContentType(file.key, file.content_type);
			const blob = await api.downloadFile(projectSlug, file.key);

			if (ct.startsWith('image/')) {
				previewUrl = URL.createObjectURL(blob);
			} else if (ct === 'application/json' || ct.startsWith('text/') || ct === 'application/javascript' || ct === 'application/xml') {
				const text = await blob.text();
				if (ct === 'application/json') {
					try { previewText = JSON.stringify(JSON.parse(text), null, 2); } catch { previewText = text; }
				} else {
					previewText = text.length > 5000 ? text.substring(0, 5000) + '\n... (truncated)' : text;
				}
			}
		} catch (err) {
			console.warn('Preview failed:', err);
		} finally {
			previewLoading = false;
		}
	}

	async function handleDownload(file: FileInfo) {
		try {
			const blob = await api.downloadFile(projectSlug, file.key);
			const url = URL.createObjectURL(blob);
			const a = document.createElement('a');
			a.href = url;
			a.download = file.key.split('/').pop() || file.key;
			document.body.appendChild(a);
			a.click();
			document.body.removeChild(a);
			URL.revokeObjectURL(url);
		} catch (err_) {
			alert(err_ instanceof Error ? err_.message : 'Download failed');
		}
	}

	async function handleDelete(file: FileInfo) {
		if (!confirm(`Delete "${file.key.split('/').pop()}"? This action cannot be undone.`)) return;
		try {
			await api.deleteFile(projectSlug, file.key);
			if (selectedFile?.key === file.key) selectedFile = null;
			await loadFiles();
		} catch (err_) {
			alert(err_ instanceof Error ? err_.message : 'Delete failed');
		}
	}

	async function handleCopyUrl(file: FileInfo) {
		const url = `${api['baseURL']}/v1/storage/${encodeURIComponent(file.key)}`;
		try {
			await navigator.clipboard.writeText(url);
		} catch {
			// Fallback: prompt
			prompt('Copy this URL:', url);
		}
		openDropdown = null;
	}

	async function handleGenerateSignedUrl(file: FileInfo, expiresIn: number) {
		signedUrlLoading = true;
		try {
			const res = await api.generateSignedUrl(projectSlug, file.key, 'download', expiresIn);
			signedUrl = res.url;
			signedUrlExpiry = res.expires_at;
		} catch (err_) {
			alert(err_ instanceof Error ? err_.message : 'Failed to generate signed URL');
		} finally {
			signedUrlLoading = false;
		}
	}

	async function handleCreateFolder() {
		if (!newFolderName.trim()) return;
		const folderKey = currentPrefix + newFolderName.trim().replace(/\/$/, '') + '/';
		try {
			// Create an empty "folder" by uploading an empty file with trailing slash key
			const emptyFile = new File([], '.folder', { type: 'application/x-directory' });
			await api.uploadFile(projectSlug, emptyFile, folderKey);
			showNewFolderModal = false;
			newFolderName = '';
			await loadFiles();
		} catch (err_) {
			alert(err_ instanceof Error ? err_.message : 'Failed to create folder');
		}
	}

	function toggleDropdown(key: string) {
		openDropdown = openDropdown === key ? null : key;
	}

	function isImageType(contentType: string): boolean {
		return contentType.startsWith('image/');
	}

	const expiryOptions = [
		{ label: '15 minutes', value: 900 },
		{ label: '1 hour', value: 3600 },
		{ label: '24 hours', value: 86400 },
		{ label: '7 days', value: 604800 }
	];
</script>

<svelte:head>
	<title>Storage - Eurobase Console</title>
</svelte:head>

<!-- svelte-ignore a11y_click_events_have_key_events -->
<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="mx-auto max-w-7xl" onclick={() => { openDropdown = null; }}>
	<!-- Page header -->
	<div class="flex items-center justify-between">
		<div>
			<h1 class="text-2xl font-bold text-gray-900">Storage</h1>
			<p class="mt-1 text-sm text-gray-500">Manage files in your project's object storage</p>
		</div>
	</div>

	<!-- Upload area -->
	<div class="mt-6">
		<FileUploader slug={projectSlug} {currentPrefix} onuploaded={loadFiles} />
	</div>

	<!-- Toolbar -->
	<div class="mt-6 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
		<!-- Breadcrumbs -->
		<nav class="flex items-center gap-1 text-sm min-w-0 overflow-x-auto">
			<button
				onclick={navigateToRoot}
				class="shrink-0 font-medium text-eurobase-600 hover:text-eurobase-700 cursor-pointer {currentPrefix === '' ? 'text-gray-900' : ''}"
			>
				Root
			</button>
			{#each breadcrumbs as crumb}
				<svg class="h-4 w-4 text-gray-400 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" d="m8.25 4.5 7.5 7.5-7.5 7.5" />
				</svg>
				<button
					onclick={() => navigateToFolder(crumb.prefix)}
					class="shrink-0 font-medium text-eurobase-600 hover:text-eurobase-700 cursor-pointer"
				>
					{crumb.label}
				</button>
			{/each}
		</nav>

		<div class="flex items-center gap-2">
			<!-- Search -->
			<div class="relative">
				<svg class="absolute left-2.5 top-1/2 -translate-y-1/2 h-4 w-4 text-gray-400" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" d="m21 21-5.197-5.197m0 0A7.5 7.5 0 1 0 5.196 5.196a7.5 7.5 0 0 0 10.607 10.607Z" />
				</svg>
				<input
					type="text"
					placeholder="Filter files..."
					bind:value={searchQuery}
					class="h-9 w-48 rounded-lg border border-gray-300 pl-8 pr-3 text-sm text-gray-900 placeholder:text-gray-400 focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none"
				/>
			</div>

			<!-- New Folder -->
			<button
				onclick={() => { showNewFolderModal = true; newFolderName = ''; }}
				class="inline-flex items-center gap-1.5 rounded-lg border border-gray-300 bg-white px-3 py-2 text-sm font-medium text-gray-700 shadow-sm hover:bg-gray-50 transition-colors cursor-pointer"
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" d="M12 10.5v6m3-3H9m4.06-7.19-2.12-2.12a1.5 1.5 0 0 0-1.061-.44H4.5A2.25 2.25 0 0 0 2.25 6v12a2.25 2.25 0 0 0 2.25 2.25h15A2.25 2.25 0 0 0 21.75 18V9a2.25 2.25 0 0 0-2.25-2.25h-5.379a1.5 1.5 0 0 1-1.06-.44Z" />
				</svg>
				New Folder
			</button>

			<!-- View toggle -->
			<div class="flex rounded-lg border border-gray-300 bg-white overflow-hidden">
				<button
					onclick={() => { viewMode = 'list'; }}
					class="p-2 cursor-pointer transition-colors {viewMode === 'list' ? 'bg-gray-100 text-gray-900' : 'text-gray-500 hover:text-gray-700'}"
					aria-label="List view"
				>
					<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M8.25 6.75h12M8.25 12h12m-12 5.25h12M3.75 6.75h.007v.008H3.75V6.75Zm.375 0a.375.375 0 1 1-.75 0 .375.375 0 0 1 .75 0ZM3.75 12h.007v.008H3.75V12Zm.375 0a.375.375 0 1 1-.75 0 .375.375 0 0 1 .75 0Zm-.375 5.25h.007v.008H3.75v-.008Zm.375 0a.375.375 0 1 1-.75 0 .375.375 0 0 1 .75 0Z" />
					</svg>
				</button>
				<button
					onclick={() => { viewMode = 'grid'; }}
					class="p-2 cursor-pointer transition-colors {viewMode === 'grid' ? 'bg-gray-100 text-gray-900' : 'text-gray-500 hover:text-gray-700'}"
					aria-label="Grid view"
				>
					<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M3.75 6A2.25 2.25 0 0 1 6 3.75h2.25A2.25 2.25 0 0 1 10.5 6v2.25a2.25 2.25 0 0 1-2.25 2.25H6a2.25 2.25 0 0 1-2.25-2.25V6ZM3.75 15.75A2.25 2.25 0 0 1 6 13.5h2.25a2.25 2.25 0 0 1 2.25 2.25V18a2.25 2.25 0 0 1-2.25 2.25H6A2.25 2.25 0 0 1 3.75 18v-2.25ZM13.5 6a2.25 2.25 0 0 1 2.25-2.25H18A2.25 2.25 0 0 1 20.25 6v2.25A2.25 2.25 0 0 1 18 10.5h-2.25a2.25 2.25 0 0 1-2.25-2.25V6ZM13.5 15.75a2.25 2.25 0 0 1 2.25-2.25H18a2.25 2.25 0 0 1 2.25 2.25V18A2.25 2.25 0 0 1 18 20.25h-2.25a2.25 2.25 0 0 1-2.25-2.25v-2.25Z" />
					</svg>
				</button>
			</div>
		</div>
	</div>

	<!-- Content area -->
	<div class="mt-4 flex gap-6">
		<!-- File list -->
		<div class="flex-1 min-w-0">
			{#if loading}
				<div class="flex flex-col items-center py-20">
					<svg class="h-10 w-10 animate-spin text-eurobase-600" fill="none" viewBox="0 0 24 24">
						<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
						<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
					</svg>
					<p class="mt-4 text-sm text-gray-500">Loading files...</p>
				</div>
			{:else if error}
				<div class="flex flex-col items-center py-20">
					<div class="flex h-16 w-16 items-center justify-center rounded-2xl bg-red-50">
						<svg class="h-8 w-8 text-red-400" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
						</svg>
					</div>
					<h3 class="mt-4 text-lg font-semibold text-gray-900">Failed to load files</h3>
					<p class="mt-2 max-w-sm text-sm text-gray-500 text-center">{error}</p>
					<button
						onclick={loadFiles}
						class="mt-4 rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-semibold text-white hover:bg-eurobase-700 transition-colors cursor-pointer"
					>
						Retry
					</button>
				</div>
			{:else if folders.length === 0 && regularFiles.length === 0}
				<!-- Empty state -->
				<div class="flex flex-col items-center py-20">
					<div class="flex h-20 w-20 items-center justify-center rounded-2xl bg-gray-100">
						<svg class="h-10 w-10 text-gray-400" fill="none" viewBox="0 0 24 24" stroke-width="1" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M2.25 12.75V12A2.25 2.25 0 0 1 4.5 9.75h15A2.25 2.25 0 0 1 21.75 12v.75m-8.69-6.44-2.12-2.12a1.5 1.5 0 0 0-1.061-.44H4.5A2.25 2.25 0 0 0 2.25 6v12a2.25 2.25 0 0 0 2.25 2.25h15A2.25 2.25 0 0 0 21.75 18V9a2.25 2.25 0 0 0-2.25-2.25h-5.379a1.5 1.5 0 0 1-1.06-.44Z" />
						</svg>
					</div>
					<h3 class="mt-4 text-lg font-semibold text-gray-900">No files yet</h3>
					<p class="mt-2 max-w-sm text-sm text-gray-500 text-center">
						Upload your first file by dragging it into the upload area above, or click the area to browse.
					</p>
				</div>
			{:else if viewMode === 'list'}
				<!-- List view -->
				<div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
					<table class="w-full text-sm">
						<thead>
							<tr class="border-b border-gray-200 bg-gray-50">
								<th class="px-4 py-3 text-left font-medium text-gray-500">Name</th>
								<th class="px-4 py-3 text-left font-medium text-gray-500 hidden sm:table-cell">Size</th>
								<th class="px-4 py-3 text-left font-medium text-gray-500 hidden md:table-cell">Type</th>
								<th class="px-4 py-3 text-left font-medium text-gray-500 hidden lg:table-cell">Last Modified</th>
								<th class="px-4 py-3 text-right font-medium text-gray-500 w-20">Actions</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-gray-100">
							{#each folders as folder}
								<tr
									class="hover:bg-gray-50 cursor-pointer transition-colors"
									onclick={() => navigateToFolder(folder.key)}
								>
									<td class="px-4 py-3">
										<div class="flex items-center gap-3">
											<svg class="h-5 w-5 text-amber-500 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
												<path stroke-linecap="round" stroke-linejoin="round" d="M2.25 12.75V12A2.25 2.25 0 0 1 4.5 9.75h15A2.25 2.25 0 0 1 21.75 12v.75m-8.69-6.44-2.12-2.12a1.5 1.5 0 0 0-1.061-.44H4.5A2.25 2.25 0 0 0 2.25 6v12a2.25 2.25 0 0 0 2.25 2.25h15A2.25 2.25 0 0 0 21.75 18V9a2.25 2.25 0 0 0-2.25-2.25h-5.379a1.5 1.5 0 0 1-1.06-.44Z" />
											</svg>
											<span class="font-medium text-gray-900">{folder.displayName}</span>
										</div>
									</td>
									<td class="px-4 py-3 text-gray-500 hidden sm:table-cell">--</td>
									<td class="px-4 py-3 text-gray-500 hidden md:table-cell">Folder</td>
									<td class="px-4 py-3 text-gray-500 hidden lg:table-cell">{formatRelativeTime(folder.last_modified)}</td>
									<td class="px-4 py-3 text-right"></td>
								</tr>
							{/each}
							{#each regularFiles as file}
								{@const icon = getFileIcon(inferContentType(file.key, file.content_type))}
								<tr
									class="hover:bg-gray-50 transition-colors {selectedFile?.key === file.key ? 'bg-eurobase-50' : ''}"
									onclick={() => selectFile(file)}
								>
									<td class="px-4 py-3">
										<div class="flex items-center gap-3">
											{#if icon === 'image'}
												<svg class="h-5 w-5 text-purple-500 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
													<path stroke-linecap="round" stroke-linejoin="round" d="m2.25 15.75 5.159-5.159a2.25 2.25 0 0 1 3.182 0l5.159 5.159m-1.5-1.5 1.409-1.409a2.25 2.25 0 0 1 3.182 0l2.909 2.909M3.75 21h16.5A2.25 2.25 0 0 0 22.5 18.75V5.25A2.25 2.25 0 0 0 20.25 3H3.75A2.25 2.25 0 0 0 1.5 5.25v13.5A2.25 2.25 0 0 0 3.75 21Z" />
												</svg>
											{:else if icon === 'pdf'}
												<svg class="h-5 w-5 text-red-500 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
													<path stroke-linecap="round" stroke-linejoin="round" d="M19.5 14.25v-2.625a3.375 3.375 0 0 0-3.375-3.375h-1.5A1.125 1.125 0 0 1 13.5 7.125v-1.5a3.375 3.375 0 0 0-3.375-3.375H8.25m2.25 0H5.625c-.621 0-1.125.504-1.125 1.125v17.25c0 .621.504 1.125 1.125 1.125h12.75c.621 0 1.125-.504 1.125-1.125V11.25a9 9 0 0 0-9-9Z" />
												</svg>
											{:else if icon === 'text'}
												<svg class="h-5 w-5 text-blue-500 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
													<path stroke-linecap="round" stroke-linejoin="round" d="M19.5 14.25v-2.625a3.375 3.375 0 0 0-3.375-3.375h-1.5A1.125 1.125 0 0 1 13.5 7.125v-1.5a3.375 3.375 0 0 0-3.375-3.375H8.25m0 12.75h7.5m-7.5 3H12M10.5 2.25H5.625c-.621 0-1.125.504-1.125 1.125v17.25c0 .621.504 1.125 1.125 1.125h12.75c.621 0 1.125-.504 1.125-1.125V11.25a9 9 0 0 0-9-9Z" />
												</svg>
											{:else}
												<svg class="h-5 w-5 text-gray-400 shrink-0" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
													<path stroke-linecap="round" stroke-linejoin="round" d="M19.5 14.25v-2.625a3.375 3.375 0 0 0-3.375-3.375h-1.5A1.125 1.125 0 0 1 13.5 7.125v-1.5a3.375 3.375 0 0 0-3.375-3.375H8.25m.75 12 3 3m0 0 3-3m-3 3v-6m-1.5-9H5.625c-.621 0-1.125.504-1.125 1.125v17.25c0 .621.504 1.125 1.125 1.125h12.75c.621 0 1.125-.504 1.125-1.125V11.25a9 9 0 0 0-9-9Z" />
												</svg>
											{/if}
											<span class="font-medium text-gray-900 truncate">{file.displayName}</span>
										</div>
									</td>
									<td class="px-4 py-3 text-gray-500 hidden sm:table-cell">{formatBytes(file.size)}</td>
									<td class="px-4 py-3 text-gray-500 hidden md:table-cell font-mono text-xs">{inferContentType(file.key, file.content_type)}</td>
									<td class="px-4 py-3 text-gray-500 hidden lg:table-cell">{formatRelativeTime(file.last_modified)}</td>
									<td class="px-4 py-3 text-right">
										<div class="relative inline-block">
											<button
												onclick={(e) => { e.stopPropagation(); toggleDropdown(file.key); }}
												class="rounded-lg p-1.5 text-gray-400 hover:text-gray-600 hover:bg-gray-100 transition-colors cursor-pointer"
												aria-label="File actions"
											>
												<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
													<path stroke-linecap="round" stroke-linejoin="round" d="M12 6.75a.75.75 0 1 1 0-1.5.75.75 0 0 1 0 1.5ZM12 12.75a.75.75 0 1 1 0-1.5.75.75 0 0 1 0 1.5ZM12 18.75a.75.75 0 1 1 0-1.5.75.75 0 0 1 0 1.5Z" />
												</svg>
											</button>
											{#if openDropdown === file.key}
												<div class="absolute right-0 top-full z-10 mt-1 w-48 rounded-lg border border-gray-200 bg-white py-1 shadow-lg">
													<button
														onclick={(e) => { e.stopPropagation(); handleDownload(file); openDropdown = null; }}
														class="flex w-full items-center gap-2 px-3 py-2 text-sm text-gray-700 hover:bg-gray-50 cursor-pointer"
													>
														<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
															<path stroke-linecap="round" stroke-linejoin="round" d="M3 16.5v2.25A2.25 2.25 0 0 0 5.25 21h13.5A2.25 2.25 0 0 0 21 18.75V16.5M16.5 12 12 16.5m0 0L7.5 12m4.5 4.5V3" />
														</svg>
														Download
													</button>
													<button
														onclick={(e) => { e.stopPropagation(); handleCopyUrl(file); }}
														class="flex w-full items-center gap-2 px-3 py-2 text-sm text-gray-700 hover:bg-gray-50 cursor-pointer"
													>
														<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
															<path stroke-linecap="round" stroke-linejoin="round" d="M13.19 8.688a4.5 4.5 0 0 1 1.242 7.244l-4.5 4.5a4.5 4.5 0 0 1-6.364-6.364l1.757-1.757m13.35-.622 1.757-1.757a4.5 4.5 0 0 0-6.364-6.364l-4.5 4.5a4.5 4.5 0 0 0 1.242 7.244" />
														</svg>
														Copy URL
													</button>
													<button
														onclick={(e) => { e.stopPropagation(); selectFile(file); openDropdown = null; }}
														class="flex w-full items-center gap-2 px-3 py-2 text-sm text-gray-700 hover:bg-gray-50 cursor-pointer"
													>
														<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
															<path stroke-linecap="round" stroke-linejoin="round" d="M16.5 10.5V6.75a4.5 4.5 0 1 0-9 0v3.75m-.75 11.25h10.5a2.25 2.25 0 0 0 2.25-2.25v-6.75a2.25 2.25 0 0 0-2.25-2.25H6.75a2.25 2.25 0 0 0-2.25 2.25v6.75a2.25 2.25 0 0 0 2.25 2.25Z" />
														</svg>
														Generate Signed URL
													</button>
													<div class="border-t border-gray-100 my-1"></div>
													<button
														onclick={(e) => { e.stopPropagation(); handleDelete(file); openDropdown = null; }}
														class="flex w-full items-center gap-2 px-3 py-2 text-sm text-red-600 hover:bg-red-50 cursor-pointer"
													>
														<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
															<path stroke-linecap="round" stroke-linejoin="round" d="m14.74 9-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 0 1-2.244 2.077H8.084a2.25 2.25 0 0 1-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 0 0-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 0 1 3.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 0 0-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 0 0-7.5 0" />
														</svg>
														Delete
													</button>
												</div>
											{/if}
										</div>
									</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			{:else}
				<!-- Grid view -->
				<div class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 gap-4">
					{#each folders as folder}
						<button
							onclick={() => navigateToFolder(folder.key)}
							class="flex flex-col items-center gap-2 rounded-xl border border-gray-200 bg-white p-4 hover:border-eurobase-300 hover:shadow-md transition-all cursor-pointer text-center"
						>
							<svg class="h-12 w-12 text-amber-500" fill="none" viewBox="0 0 24 24" stroke-width="1" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" d="M2.25 12.75V12A2.25 2.25 0 0 1 4.5 9.75h15A2.25 2.25 0 0 1 21.75 12v.75m-8.69-6.44-2.12-2.12a1.5 1.5 0 0 0-1.061-.44H4.5A2.25 2.25 0 0 0 2.25 6v12a2.25 2.25 0 0 0 2.25 2.25h15A2.25 2.25 0 0 0 21.75 18V9a2.25 2.25 0 0 0-2.25-2.25h-5.379a1.5 1.5 0 0 1-1.06-.44Z" />
							</svg>
							<p class="text-sm font-medium text-gray-900 truncate w-full">{folder.displayName}</p>
						</button>
					{/each}
					{#each regularFiles as file}
						{@const icon = getFileIcon(inferContentType(file.key, file.content_type))}
						<div
							role="button"
							tabindex="0"
							class="flex flex-col items-center gap-2 rounded-xl border bg-white p-4 transition-all cursor-pointer text-center {selectedFile?.key === file.key ? 'border-eurobase-500 ring-2 ring-eurobase-500/20' : 'border-gray-200 hover:border-eurobase-300 hover:shadow-md'}"
							onclick={() => selectFile(file)}
							ondblclick={() => handleDownload(file)}
							onkeydown={(e) => { if (e.key === 'Enter') selectFile(file); }}
						>
							{#if icon === 'image'}
								<div class="flex h-16 w-16 items-center justify-center rounded-lg bg-purple-50">
									<svg class="h-8 w-8 text-purple-500" fill="none" viewBox="0 0 24 24" stroke-width="1" stroke="currentColor">
										<path stroke-linecap="round" stroke-linejoin="round" d="m2.25 15.75 5.159-5.159a2.25 2.25 0 0 1 3.182 0l5.159 5.159m-1.5-1.5 1.409-1.409a2.25 2.25 0 0 1 3.182 0l2.909 2.909M3.75 21h16.5A2.25 2.25 0 0 0 22.5 18.75V5.25A2.25 2.25 0 0 0 20.25 3H3.75A2.25 2.25 0 0 0 1.5 5.25v13.5A2.25 2.25 0 0 0 3.75 21Z" />
									</svg>
								</div>
							{:else if icon === 'pdf'}
								<div class="flex h-16 w-16 items-center justify-center rounded-lg bg-red-50">
									<svg class="h-8 w-8 text-red-500" fill="none" viewBox="0 0 24 24" stroke-width="1" stroke="currentColor">
										<path stroke-linecap="round" stroke-linejoin="round" d="M19.5 14.25v-2.625a3.375 3.375 0 0 0-3.375-3.375h-1.5A1.125 1.125 0 0 1 13.5 7.125v-1.5a3.375 3.375 0 0 0-3.375-3.375H8.25m2.25 0H5.625c-.621 0-1.125.504-1.125 1.125v17.25c0 .621.504 1.125 1.125 1.125h12.75c.621 0 1.125-.504 1.125-1.125V11.25a9 9 0 0 0-9-9Z" />
									</svg>
								</div>
							{:else}
								<div class="flex h-16 w-16 items-center justify-center rounded-lg bg-gray-100">
									<svg class="h-8 w-8 text-gray-400" fill="none" viewBox="0 0 24 24" stroke-width="1" stroke="currentColor">
										<path stroke-linecap="round" stroke-linejoin="round" d="M19.5 14.25v-2.625a3.375 3.375 0 0 0-3.375-3.375h-1.5A1.125 1.125 0 0 1 13.5 7.125v-1.5a3.375 3.375 0 0 0-3.375-3.375H8.25m.75 12 3 3m0 0 3-3m-3 3v-6m-1.5-9H5.625c-.621 0-1.125.504-1.125 1.125v17.25c0 .621.504 1.125 1.125 1.125h12.75c.621 0 1.125-.504 1.125-1.125V11.25a9 9 0 0 0-9-9Z" />
									</svg>
								</div>
							{/if}
							<div class="w-full min-w-0">
								<p class="text-sm font-medium text-gray-900 truncate">{file.displayName}</p>
								<p class="text-xs text-gray-500">{formatBytes(file.size)}</p>
							</div>
						</div>
					{/each}
				</div>
			{/if}
		</div>

		<!-- Preview sidebar -->
		{#if selectedFile}
			<div class="hidden lg:block w-80 shrink-0">
				<div class="rounded-xl border border-gray-200 bg-white p-5 sticky top-6">
					<!-- Preview area -->
					<div class="flex justify-center mb-4">
						{#if previewLoading}
							<div class="flex h-40 w-full items-center justify-center rounded-lg bg-gray-50">
								<svg class="h-6 w-6 animate-spin text-gray-400" fill="none" viewBox="0 0 24 24">
									<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
									<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
								</svg>
							</div>
						{:else if previewUrl}
							<div class="flex h-48 w-full items-center justify-center rounded-lg bg-gray-50 overflow-hidden">
								<img
									src={previewUrl}
									alt={selectedFile.key.split('/').pop()}
									class="max-h-full max-w-full object-contain"
								/>
							</div>
						{:else if previewText !== null}
							<div class="w-full max-h-48 overflow-auto rounded-lg bg-gray-900 p-3">
								<pre class="text-xs text-green-400 whitespace-pre-wrap break-all">{previewText}</pre>
							</div>
						{:else}
							{@const icon = getFileIcon(inferContentType(selectedFile.key, selectedFile.content_type))}
							<div class="flex h-32 w-full items-center justify-center rounded-lg bg-gray-50">
								<svg class="h-16 w-16 {icon === 'pdf' ? 'text-red-400' : icon === 'text' ? 'text-blue-400' : 'text-gray-300'}" fill="none" viewBox="0 0 24 24" stroke-width="1" stroke="currentColor">
									<path stroke-linecap="round" stroke-linejoin="round" d="M19.5 14.25v-2.625a3.375 3.375 0 0 0-3.375-3.375h-1.5A1.125 1.125 0 0 1 13.5 7.125v-1.5a3.375 3.375 0 0 0-3.375-3.375H8.25m2.25 0H5.625c-.621 0-1.125.504-1.125 1.125v17.25c0 .621.504 1.125 1.125 1.125h12.75c.621 0 1.125-.504 1.125-1.125V11.25a9 9 0 0 0-9-9Z" />
								</svg>
							</div>
						{/if}
					</div>

					<!-- File details -->
					<div class="space-y-3">
						<h3 class="text-sm font-semibold text-gray-900 break-all">
							{selectedFile.key.split('/').pop()}
						</h3>
						<dl class="space-y-2 text-sm">
							<div class="flex justify-between">
								<dt class="text-gray-500">Size</dt>
								<dd class="text-gray-900 font-medium">{formatBytes(selectedFile.size)}</dd>
							</div>
							<div class="flex justify-between">
								<dt class="text-gray-500">Type</dt>
								<dd class="text-gray-900 font-mono text-xs">{inferContentType(selectedFile.key, selectedFile.content_type)}</dd>
							</div>
							<div class="flex justify-between">
								<dt class="text-gray-500">Modified</dt>
								<dd class="text-gray-900">{formatRelativeTime(selectedFile.last_modified)}</dd>
							</div>
							<div>
								<dt class="text-gray-500">Key</dt>
								<dd class="text-gray-700 font-mono text-xs mt-0.5 break-all">{selectedFile.key}</dd>
							</div>
						</dl>
					</div>

					<!-- Action buttons -->
					<div class="mt-5 space-y-2">
						<button
							onclick={() => handleDownload(selectedFile!)}
							class="flex w-full items-center justify-center gap-2 rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-semibold text-white hover:bg-eurobase-700 transition-colors cursor-pointer"
						>
							<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" d="M3 16.5v2.25A2.25 2.25 0 0 0 5.25 21h13.5A2.25 2.25 0 0 0 21 18.75V16.5M16.5 12 12 16.5m0 0L7.5 12m4.5 4.5V3" />
							</svg>
							Download
						</button>
						<button
							onclick={() => handleCopyUrl(selectedFile!)}
							class="flex w-full items-center justify-center gap-2 rounded-lg border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors cursor-pointer"
						>
							<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" d="M13.19 8.688a4.5 4.5 0 0 1 1.242 7.244l-4.5 4.5a4.5 4.5 0 0 1-6.364-6.364l1.757-1.757m13.35-.622 1.757-1.757a4.5 4.5 0 0 0-6.364-6.364l-4.5 4.5a4.5 4.5 0 0 0 1.242 7.244" />
							</svg>
							Copy URL
						</button>
						<button
							onclick={() => handleDelete(selectedFile!)}
							class="flex w-full items-center justify-center gap-2 rounded-lg border border-red-200 bg-white px-4 py-2 text-sm font-medium text-red-600 hover:bg-red-50 transition-colors cursor-pointer"
						>
							<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" d="m14.74 9-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 0 1-2.244 2.077H8.084a2.25 2.25 0 0 1-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 0 0-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 0 1 3.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 0 0-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 0 0-7.5 0" />
							</svg>
							Delete
						</button>
					</div>

					<!-- Signed URL generator -->
					<div class="mt-5 border-t border-gray-200 pt-4">
						<h4 class="text-sm font-medium text-gray-700 mb-2">Generate Signed URL</h4>
						<div class="flex gap-2">
							<select
								bind:value={selectedExpiry}
								class="flex-1 rounded-lg border border-gray-300 px-2 py-1.5 text-sm text-gray-700 focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none"
							>
								{#each expiryOptions as opt}
									<option value={opt.value}>{opt.label}</option>
								{/each}
							</select>
							<button
								onclick={() => handleGenerateSignedUrl(selectedFile!, selectedExpiry)}
								disabled={signedUrlLoading}
								class="rounded-lg bg-gray-100 px-3 py-1.5 text-sm font-medium text-gray-700 hover:bg-gray-200 transition-colors cursor-pointer disabled:opacity-50"
							>
								{#if signedUrlLoading}...{:else}Generate{/if}
							</button>
						</div>
						{#if signedUrl}
							<div class="mt-2 rounded-lg bg-gray-50 border border-gray-200 p-2">
								<input
									type="text"
									readonly
									value={signedUrl}
									class="w-full text-xs font-mono text-gray-700 bg-transparent border-none focus:outline-none"
									onclick={(e) => { (e.target as HTMLInputElement).select(); }}
								/>
								<p class="mt-1 text-xs text-gray-400">
									Expires: {new Date(signedUrlExpiry).toLocaleString('en-GB')}
								</p>
							</div>
						{/if}
					</div>

					<!-- Close button -->
					<button
						onclick={() => { selectedFile = null; signedUrl = ''; if (previewUrl) { URL.revokeObjectURL(previewUrl); previewUrl = null; } previewText = null; }}
						class="mt-4 w-full text-center text-xs text-gray-400 hover:text-gray-600 cursor-pointer"
					>
						Close panel
					</button>
				</div>
			</div>
		{/if}
	</div>
</div>

<!-- New Folder Modal -->
{#if showNewFolderModal}
	<!-- svelte-ignore a11y_click_events_have_key_events -->
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<div class="absolute inset-0 bg-black/50" onclick={() => { showNewFolderModal = false; }}></div>
		<div class="relative w-full max-w-sm rounded-2xl bg-white p-6 shadow-xl mx-4">
			<h2 class="text-lg font-semibold text-gray-900">New Folder</h2>
			<p class="mt-1 text-sm text-gray-500">Create a new folder in the current directory.</p>
			<form
				onsubmit={(e) => { e.preventDefault(); handleCreateFolder(); }}
				class="mt-4"
			>
				<input
					type="text"
					bind:value={newFolderName}
					required
					placeholder="folder-name"
					class="block w-full rounded-lg border border-gray-300 px-3.5 py-2.5 text-sm text-gray-900 shadow-sm placeholder:text-gray-400 focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none"
				/>
				<div class="mt-4 flex justify-end gap-3">
					<button
						type="button"
						onclick={() => { showNewFolderModal = false; }}
						class="rounded-lg border border-gray-300 px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors cursor-pointer"
					>
						Cancel
					</button>
					<button
						type="submit"
						disabled={!newFolderName.trim()}
						class="rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 transition-colors disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
					>
						Create
					</button>
				</div>
			</form>
		</div>
	</div>
{/if}
