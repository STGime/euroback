<script lang="ts">
	import { page } from '$app/stores';
	import { api, type EndUser, type Project } from '$lib/api.js';
	import { getContext, onMount } from 'svelte';

	const projectCtx = getContext<{ id: string; project: Project | null }>('projectId');

	let projectId = $derived($page.params.id);

	let users: EndUser[] = $state([]);
	let total = $state(0);
	let loading = $state(true);
	let error: string | null = $state(null);

	// Search & pagination
	let search = $state('');
	let searchTimeout: ReturnType<typeof setTimeout> | null = $state(null);
	let currentPage = $state(0);
	const pageSize = 50;

	// Create form
	let showCreate = $state(false);
	let createEmail = $state('');
	let createPassword = $state('');
	let createMetadata = $state('');
	let createError: string | null = $state(null);
	let creating = $state(false);

	// Edit form
	let editUser: EndUser | null = $state(null);
	let editEmail = $state('');
	let editDisplayName = $state('');
	let editMetadata = $state('');
	let editError: string | null = $state(null);
	let saving = $state(false);

	// Password reset
	let resetUser: EndUser | null = $state(null);
	let resetPassword = $state('');
	let resetConfirmPassword = $state('');
	let resetError: string | null = $state(null);
	let resetting = $state(false);

	// Delete confirm
	let deleteConfirmUser: EndUser | null = $state(null);
	let deleting = $state(false);

	// Detail view
	let selectedUser: EndUser | null = $state(null);

	onMount(() => { loadUsers(); });

	async function loadUsers() {
		loading = true;
		error = null;
		try {
			const result = await api.listEndUsers(projectId, {
				search: search || undefined,
				limit: pageSize,
				offset: currentPage * pageSize
			});
			users = result.users;
			total = result.total;
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load users';
		} finally {
			loading = false;
		}
	}

	function handleSearchInput(value: string) {
		search = value;
		if (searchTimeout) clearTimeout(searchTimeout);
		searchTimeout = setTimeout(() => {
			currentPage = 0;
			loadUsers();
		}, 300);
	}

	function goToPage(p: number) {
		currentPage = p;
		loadUsers();
	}

	let totalPages = $derived(Math.ceil(total / pageSize));

	async function handleCreate() {
		if (!createEmail.trim() || !createPassword) return;
		creating = true;
		createError = null;
		try {
			let metadata: Record<string, any> | undefined;
			if (createMetadata.trim()) {
				try {
					metadata = JSON.parse(createMetadata);
				} catch {
					createError = 'Invalid JSON in metadata field';
					creating = false;
					return;
				}
			}
			await api.createEndUser(projectId, {
				email: createEmail.trim(),
				password: createPassword,
				metadata
			});
			showCreate = false;
			createEmail = '';
			createPassword = '';
			createMetadata = '';
			await loadUsers();
		} catch (err) {
			let msg = err instanceof Error ? err.message : 'Failed to create user';
			const m = msg.match(/\{"error":"(.+?)"\}/);
			if (m) msg = m[1];
			createError = msg;
		} finally {
			creating = false;
		}
	}

	function openEdit(user: EndUser) {
		editUser = user;
		editEmail = user.email ?? '';
		editDisplayName = user.display_name ?? '';
		editMetadata = JSON.stringify(user.metadata, null, 2);
		editError = null;
	}

	async function handleEdit() {
		if (!editUser) return;
		saving = true;
		editError = null;
		try {
			let metadata: Record<string, any> | undefined;
			if (editMetadata.trim()) {
				try {
					metadata = JSON.parse(editMetadata);
				} catch {
					editError = 'Invalid JSON in metadata field';
					saving = false;
					return;
				}
			}
			const data: { email?: string; display_name?: string; metadata?: Record<string, any> } = {};
			if (editEmail.trim() !== editUser.email) data.email = editEmail.trim();
			if (editDisplayName !== (editUser.display_name ?? '')) data.display_name = editDisplayName;
			if (metadata !== undefined) data.metadata = metadata;

			if (Object.keys(data).length === 0) {
				editUser = null;
				saving = false;
				return;
			}

			await api.updateEndUser(projectId, editUser.id, data);
			editUser = null;
			await loadUsers();
		} catch (err) {
			let msg = err instanceof Error ? err.message : 'Failed to update user';
			const m = msg.match(/\{"error":"(.+?)"\}/);
			if (m) msg = m[1];
			editError = msg;
		} finally {
			saving = false;
		}
	}

	async function handleToggleSuspend(user: EndUser) {
		try {
			if (user.banned_at) {
				await api.unsuspendEndUser(projectId, user.id);
			} else {
				await api.suspendEndUser(projectId, user.id);
			}
			await loadUsers();
		} catch { /* ignore */ }
	}

	let resetPasswordsMismatch = $derived(resetConfirmPassword.length > 0 && resetPassword !== resetConfirmPassword);

	async function handleResetPassword() {
		if (!resetUser || !resetPassword) return;
		if (resetPassword !== resetConfirmPassword) {
			resetError = 'Passwords do not match';
			return;
		}
		resetting = true;
		resetError = null;
		try {
			await api.resetEndUserPassword(projectId, resetUser.id, resetPassword);
			resetUser = null;
			resetPassword = '';
			resetConfirmPassword = '';
		} catch (err) {
			let msg = err instanceof Error ? err.message : 'Failed to reset password';
			const m = msg.match(/\{"error":"(.+?)"\}/);
			if (m) msg = m[1];
			resetError = msg;
		} finally {
			resetting = false;
		}
	}

	async function handleDelete() {
		if (!deleteConfirmUser) return;
		deleting = true;
		try {
			await api.deleteEndUser(projectId, deleteConfirmUser.id);
			if (selectedUser?.id === deleteConfirmUser.id) selectedUser = null;
			deleteConfirmUser = null;
			await loadUsers();
		} catch { /* ignore */ } finally {
			deleting = false;
		}
	}

	function formatDate(dateStr: string) {
		return new Date(dateStr).toLocaleDateString('en-GB', {
			day: 'numeric', month: 'short', year: 'numeric'
		});
	}

	function formatDateTime(dateStr: string | null) {
		if (!dateStr) return 'Never';
		return new Date(dateStr).toLocaleString('en-GB', {
			day: 'numeric', month: 'short', year: 'numeric',
			hour: '2-digit', minute: '2-digit'
		});
	}

	let copiedStep: number | null = $state(null);

	function copySnippet(code: string, step: number) {
		navigator.clipboard.writeText(code);
		copiedStep = step;
		setTimeout(() => { if (copiedStep === step) copiedStep = null; }, 1500);
	}

	let slug = $derived(projectCtx.project?.slug ?? 'my-project');

	let quickstartSteps = $derived([
		{ title: 'Install the SDK', desc: 'Add the Eurobase SDK to your project.', code: 'npm install @eurobase/sdk' },
		{ title: 'Initialize the client', desc: 'Create a client with your project URL and API key.', code: `import { createClient } from '@eurobase/sdk'\n\nconst eb = createClient({\n  url: 'https://${slug}.eurobase.app',\n  apiKey: process.env.EUROBASE_PUBLIC_KEY\n})` },
		{ title: 'Sign up a user', desc: 'Register a new user with email and password.', code: `const { data, error } = await eb.auth.signUp({\n  email: 'user@example.com',\n  password: 'securepassword'\n})` },
		{ title: 'Sign in', desc: 'Authenticate an existing user.', code: `const { data, error } = await eb.auth.signIn({\n  email: 'user@example.com',\n  password: 'securepassword'\n})` },
		{ title: 'Listen for auth changes', desc: 'React to sign-in, sign-out, and token refresh events.', code: `eb.auth.onAuthStateChange((event, session) => {\n  console.log(event) // SIGNED_IN | SIGNED_OUT | TOKEN_REFRESHED\n})\n\n// Get the current user at any time\nconst { data: user } = await eb.auth.getUser()` },
		{ title: 'Sign out', desc: 'End the user session and clear tokens.', code: 'await eb.auth.signOut()' },
		{ title: 'Authenticated queries', desc: 'After sign-in, the JWT is sent automatically with every query. RLS policies are enforced server-side.', code: `// No extra headers needed — the SDK handles auth automatically\nconst { data } = await eb.db.from('todos').select('*')\n// Only rows allowed by your RLS policies are returned` },
	]);
</script>

<div class="mx-auto max-w-5xl space-y-6">
	<!-- Header -->
	<div class="flex items-center justify-between">
		<div class="flex items-center gap-3">
			<div>
				<h2 class="text-lg font-semibold text-gray-900">Authentication</h2>
				<p class="text-sm text-gray-500">Manage users who sign in to your application via the Eurobase Auth SDK. These users are stored in your project's <code class="rounded bg-gray-100 px-1 py-0.5 text-xs font-mono">users</code> table and authenticated via <code class="rounded bg-gray-100 px-1 py-0.5 text-xs font-mono">/v1/auth</code>.</p>
			</div>
			{#if !loading && total > 0}
				<span class="rounded-full bg-gray-100 px-2.5 py-0.5 text-xs font-medium text-gray-600">{total}</span>
			{/if}
		</div>
		<button
			type="button"
			class="cursor-pointer inline-flex items-center gap-1.5 rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 transition-colors"
			onclick={() => { showCreate = true; createError = null; }}
		>
			<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
			</svg>
			Add User
		</button>
	</div>

	<!-- Quick Start Guide -->
	<details class="rounded-xl border border-gray-200 bg-white">
		<summary class="cursor-pointer select-none px-5 py-4 text-sm font-semibold text-gray-900 hover:bg-gray-50 transition-colors">
			Quick Start — Add Auth to Your App
		</summary>
		<div class="border-t border-gray-200 px-5 py-5 space-y-5">
			{#each quickstartSteps as step, i}
				<div class="flex gap-4">
					<div class="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700">{i + 1}</div>
					<div class="flex-1 min-w-0">
						<h4 class="text-sm font-semibold text-gray-900">{step.title}</h4>
						<p class="mt-0.5 text-xs text-gray-500">{step.desc}</p>
						<div class="relative mt-2">
							<pre class="rounded-lg bg-gray-900 p-3 text-xs font-mono text-green-400 overflow-x-auto">{step.code}</pre>
							<button
								type="button"
								class="cursor-pointer absolute top-2 right-2 rounded-md bg-gray-800 px-2 py-0.5 text-[10px] font-medium text-gray-400 hover:text-white transition-colors"
								onclick={() => copySnippet(step.code, i)}
							>
								{copiedStep === i ? 'Copied!' : 'Copy'}
							</button>
						</div>
					</div>
				</div>
			{/each}
		</div>
	</details>

	<!-- Search -->
	{#if !loading || users.length > 0 || search}
		<div class="relative">
			<svg class="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-gray-400" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" d="m21 21-5.197-5.197m0 0A7.5 7.5 0 1 0 5.196 5.196a7.5 7.5 0 0 0 10.607 10.607Z" />
			</svg>
			<input
				type="text"
				value={search}
				oninput={(e) => handleSearchInput(e.currentTarget.value)}
				placeholder="Search by email or name..."
				class="w-full rounded-lg border border-gray-300 py-2 pl-10 pr-3 text-sm text-gray-900 placeholder-gray-400 focus:border-eurobase-500 focus:outline-none"
			/>
		</div>
	{/if}

	<!-- Error -->
	{#if error}
		<div class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">{error}</div>
	{/if}

	<!-- User list -->
	{#if loading && users.length === 0}
		<div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
			{#each Array(3) as _}
				<div class="flex items-center gap-4 px-5 py-4 border-b border-gray-100 last:border-b-0">
					<div class="h-8 w-8 animate-pulse rounded-full bg-gray-200"></div>
					<div class="flex-1 space-y-2">
						<div class="h-4 w-48 animate-pulse rounded bg-gray-200"></div>
						<div class="h-3 w-32 animate-pulse rounded bg-gray-100"></div>
					</div>
				</div>
			{/each}
		</div>
	{:else if users.length === 0 && !search}
		<div class="rounded-xl border border-gray-200 bg-white p-12 text-center">
			<svg class="mx-auto h-12 w-12 text-gray-300" fill="none" viewBox="0 0 24 24" stroke-width="1" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" d="M15 19.128a9.38 9.38 0 0 0 2.625.372 9.337 9.337 0 0 0 4.121-.952 4.125 4.125 0 0 0-7.533-2.493M15 19.128v-.003c0-1.113-.285-2.16-.786-3.07M15 19.128v.106A12.318 12.318 0 0 1 8.624 21c-2.331 0-4.512-.645-6.374-1.766l-.001-.109a6.375 6.375 0 0 1 11.964-3.07M12 6.375a3.375 3.375 0 1 1-6.75 0 3.375 3.375 0 0 1 6.75 0Zm8.25 2.25a2.625 2.625 0 1 1-5.25 0 2.625 2.625 0 0 1 5.25 0Z" />
			</svg>
			<h3 class="mt-3 text-sm font-semibold text-gray-700">No users yet</h3>
			<p class="mt-1 text-sm text-gray-400">Users will appear here when they sign up via your app, or you can invite them manually.</p>
			<button
				type="button"
				class="cursor-pointer mt-4 inline-flex items-center gap-1.5 rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 transition-colors"
				onclick={() => { showCreate = true; createError = null; }}
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
				</svg>
				Add User
			</button>
		</div>
	{:else if users.length === 0 && search}
		<div class="rounded-xl border border-gray-200 bg-white p-12 text-center">
			<p class="text-sm text-gray-500">No users matching "<strong>{search}</strong>"</p>
		</div>
	{:else}
		<div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
			<table class="w-full">
				<thead>
					<tr class="border-b border-gray-200 bg-gray-50/50">
						<th class="px-5 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Email / Phone</th>
						<th class="px-5 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Provider</th>
						<th class="px-5 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Display Name</th>
						<th class="px-5 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Status</th>
						<th class="px-5 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Last Sign In</th>
						<th class="px-5 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Created</th>
						<th class="px-5 py-3 text-right text-xs font-medium uppercase tracking-wider text-gray-500">Actions</th>
					</tr>
				</thead>
				<tbody class="divide-y divide-gray-100">
					{#each users as user}
						<tr class="hover:bg-gray-50/50 transition-colors">
							<td class="px-5 py-3">
								<button
									type="button"
									class="cursor-pointer flex items-center gap-3 text-left"
									onclick={() => (selectedUser = selectedUser?.id === user.id ? null : user)}
								>
									<div class="flex h-8 w-8 items-center justify-center rounded-full {user.banned_at ? 'bg-red-100 text-red-700' : 'bg-eurobase-100 text-eurobase-700'} text-xs font-semibold">
										{(user.email ?? user.phone ?? '?')[0].toUpperCase()}
									</div>
									<span class="text-sm font-medium text-gray-900">{user.email ?? user.phone ?? '—'}</span>
								</button>
							</td>
							<td class="px-5 py-3">
								<div class="flex flex-wrap gap-1">
									{#each (user.providers?.length ? user.providers : ['email']) as provider}
										<span class="inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium
											{provider === 'google' ? 'bg-blue-50 text-blue-700' :
											 provider === 'github' ? 'bg-gray-100 text-gray-700' :
											 provider === 'linkedin' ? 'bg-sky-50 text-sky-700' :
											 provider === 'apple' ? 'bg-gray-100 text-gray-900' :
											 provider === 'phone' ? 'bg-amber-50 text-amber-700' :
											 'bg-purple-50 text-purple-700'}">
											{provider}
										</span>
									{/each}
								</div>
							</td>
							<td class="px-5 py-3 text-sm text-gray-500">{user.display_name ?? '—'}</td>
							<td class="px-5 py-3">
								{#if user.banned_at}
									<span class="inline-flex items-center rounded-full bg-red-100 px-2 py-0.5 text-xs font-medium text-red-700">Suspended</span>
								{:else}
									<span class="inline-flex items-center rounded-full bg-green-100 px-2 py-0.5 text-xs font-medium text-green-700">Active</span>
								{/if}
							</td>
							<td class="px-5 py-3 text-sm text-gray-500">{formatDateTime(user.last_sign_in_at)}</td>
							<td class="px-5 py-3 text-sm text-gray-500">{formatDate(user.created_at)}</td>
							<td class="px-5 py-3">
								<div class="flex items-center justify-end gap-1">
									<!-- Edit -->
									<button
										type="button"
										class="cursor-pointer rounded p-1.5 text-gray-300 hover:bg-gray-100 hover:text-gray-600 transition-colors"
										onclick={() => openEdit(user)}
										title="Edit user"
									>
										<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
											<path stroke-linecap="round" stroke-linejoin="round" d="m16.862 4.487 1.687-1.688a1.875 1.875 0 1 1 2.652 2.652L10.582 16.07a4.5 4.5 0 0 1-1.897 1.13L6 18l.8-2.685a4.5 4.5 0 0 1 1.13-1.897l8.932-8.931Zm0 0L19.5 7.125M18 14v4.75A2.25 2.25 0 0 1 15.75 21H5.25A2.25 2.25 0 0 1 3 18.75V8.25A2.25 2.25 0 0 1 5.25 6H10" />
										</svg>
									</button>
									<!-- Reset password -->
									<button
										type="button"
										class="cursor-pointer rounded p-1.5 text-gray-300 hover:bg-gray-100 hover:text-gray-600 transition-colors"
										onclick={() => { resetUser = user; resetPassword = ''; resetConfirmPassword = ''; resetError = null; }}
										title="Reset password"
									>
										<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
											<path stroke-linecap="round" stroke-linejoin="round" d="M15.75 5.25a3 3 0 0 1 3 3m3 0a6 6 0 0 1-7.029 5.912c-.563-.097-1.159.026-1.563.43L10.5 17.25H8.25v2.25H6v2.25H2.25v-2.818c0-.597.237-1.17.659-1.591l6.499-6.499c.404-.404.527-1 .43-1.563A6 6 0 1 1 21.75 8.25Z" />
										</svg>
									</button>
									<!-- Suspend/unsuspend -->
									<button
										type="button"
										class="cursor-pointer rounded p-1.5 transition-colors {user.banned_at ? 'text-green-400 hover:bg-green-50 hover:text-green-600' : 'text-gray-300 hover:bg-amber-50 hover:text-amber-600'}"
										onclick={() => handleToggleSuspend(user)}
										title={user.banned_at ? 'Unsuspend user' : 'Suspend user'}
									>
										{#if user.banned_at}
											<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
												<path stroke-linecap="round" stroke-linejoin="round" d="M13.5 10.5V6.75a4.5 4.5 0 1 1 9 0v3.75M3.75 21.75h10.5a2.25 2.25 0 0 0 2.25-2.25v-6.75a2.25 2.25 0 0 0-2.25-2.25H3.75a2.25 2.25 0 0 0-2.25 2.25v6.75a2.25 2.25 0 0 0 2.25 2.25Z" />
											</svg>
										{:else}
											<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
												<path stroke-linecap="round" stroke-linejoin="round" d="M18.364 18.364A9 9 0 0 0 5.636 5.636m12.728 12.728A9 9 0 0 1 5.636 5.636m12.728 12.728L5.636 5.636" />
											</svg>
										{/if}
									</button>
									<!-- Delete -->
									<button
										type="button"
										class="cursor-pointer rounded p-1.5 text-gray-300 hover:bg-red-50 hover:text-red-500 transition-colors"
										onclick={() => (deleteConfirmUser = user)}
										title="Delete user"
									>
										<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
											<path stroke-linecap="round" stroke-linejoin="round" d="m14.74 9-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 0 1-2.244 2.077H8.084a2.25 2.25 0 0 1-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 0 0-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 0 1 3.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 0 0-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 0 0-7.5 0" />
										</svg>
									</button>
								</div>
							</td>
						</tr>
						<!-- Detail row -->
						{#if selectedUser?.id === user.id}
							<tr>
								<td colspan="7" class="bg-gray-50 px-5 py-4">
									<div class="grid grid-cols-2 gap-6">
										<div>
											<h4 class="text-xs font-semibold uppercase tracking-wider text-gray-500 mb-2">User Details</h4>
											<dl class="space-y-1.5 text-sm">
												<div class="flex gap-2">
													<dt class="text-gray-400 w-24 shrink-0">ID</dt>
													<dd class="font-mono text-xs text-gray-600">{user.id}</dd>
												</div>
												<div class="flex gap-2">
													<dt class="text-gray-400 w-24 shrink-0">Email</dt>
													<dd class="text-gray-900">{user.email ?? '—'}</dd>
												</div>
												<div class="flex gap-2">
													<dt class="text-gray-400 w-24 shrink-0">Provider</dt>
													<dd class="flex flex-wrap gap-1">
														{#each (user.providers?.length ? user.providers : ['email']) as provider}
															<span class="inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium
																{provider === 'google' ? 'bg-blue-50 text-blue-700' :
																 provider === 'github' ? 'bg-gray-100 text-gray-700' :
																 provider === 'linkedin' ? 'bg-sky-50 text-sky-700' :
																 provider === 'apple' ? 'bg-gray-100 text-gray-900' :
																 provider === 'phone' ? 'bg-amber-50 text-amber-700' :
																 'bg-purple-50 text-purple-700'}">
																{provider}
															</span>
														{/each}
													</dd>
												</div>
												<div class="flex gap-2">
													<dt class="text-gray-400 w-24 shrink-0">Display Name</dt>
													<dd class="text-gray-900">{user.display_name ?? '—'}</dd>
												</div>
												<div class="flex gap-2">
													<dt class="text-gray-400 w-24 shrink-0">Status</dt>
													<dd>{user.banned_at ? `Suspended since ${formatDateTime(user.banned_at)}` : 'Active'}</dd>
												</div>
												<div class="flex gap-2">
													<dt class="text-gray-400 w-24 shrink-0">Last Sign In</dt>
													<dd class="text-gray-900">{formatDateTime(user.last_sign_in_at)}</dd>
												</div>
												<div class="flex gap-2">
													<dt class="text-gray-400 w-24 shrink-0">Created</dt>
													<dd class="text-gray-900">{formatDateTime(user.created_at)}</dd>
												</div>
											</dl>
										</div>
										<div>
											<h4 class="text-xs font-semibold uppercase tracking-wider text-gray-500 mb-2">Metadata</h4>
											{#if Object.keys(user.metadata).length === 0}
												<p class="text-xs text-gray-400">No metadata set.</p>
											{:else}
												<pre class="rounded-lg border border-gray-200 bg-white p-3 text-xs font-mono text-gray-700 overflow-auto max-h-48">{JSON.stringify(user.metadata, null, 2)}</pre>
											{/if}
										</div>
									</div>
								</td>
							</tr>
						{/if}
					{/each}
				</tbody>
			</table>
		</div>

		<!-- Pagination -->
		{#if totalPages > 1}
			<div class="flex items-center justify-between">
				<p class="text-sm text-gray-500">
					Showing {currentPage * pageSize + 1}–{Math.min((currentPage + 1) * pageSize, total)} of {total}
				</p>
				<div class="flex gap-1">
					<button
						type="button"
						class="cursor-pointer rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors disabled:opacity-40 disabled:cursor-default"
						disabled={currentPage === 0}
						onclick={() => goToPage(currentPage - 1)}
					>Previous</button>
					<button
						type="button"
						class="cursor-pointer rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors disabled:opacity-40 disabled:cursor-default"
						disabled={currentPage >= totalPages - 1}
						onclick={() => goToPage(currentPage + 1)}
					>Next</button>
				</div>
			</div>
		{/if}
	{/if}
</div>

<!-- Create User Modal -->
{#if showCreate}
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<button type="button" class="fixed inset-0 bg-black/50 cursor-default" onclick={() => (showCreate = false)} tabindex="-1" aria-label="Close"></button>
		<div class="relative z-10 w-full max-w-md rounded-xl bg-white shadow-2xl">
			<div class="flex items-center justify-between border-b border-gray-200 px-6 py-4">
				<h2 class="text-lg font-semibold text-gray-900">Add User</h2>
				<button type="button" class="cursor-pointer rounded-lg p-1.5 text-gray-400 hover:bg-gray-100 hover:text-gray-600" onclick={() => (showCreate = false)} aria-label="Close">
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12" /></svg>
				</button>
			</div>
			<div class="px-6 py-5 space-y-4">
				{#if createError}
					<div class="flex items-start gap-2 rounded-lg border border-red-200 bg-red-50 px-4 py-3">
						<svg class="h-4 w-4 mt-0.5 shrink-0 text-red-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
						</svg>
						<p class="text-sm text-red-700">{createError}</p>
					</div>
				{/if}
				<div>
					<label for="user-email" class="block text-sm font-medium text-gray-700 mb-1">Email</label>
					<input id="user-email" type="email" bind:value={createEmail} placeholder="user@example.com"
						class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 placeholder-gray-300 focus:border-eurobase-500 focus:outline-none" />
				</div>
				<div>
					<label for="user-password" class="block text-sm font-medium text-gray-700 mb-1">Password</label>
					<input id="user-password" type="password" bind:value={createPassword} placeholder="Minimum 8 characters"
						class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 placeholder-gray-300 focus:border-eurobase-500 focus:outline-none" />
					<p class="mt-1 text-xs text-gray-400">Must be at least 8 characters. The user can change it after signing in.</p>
				</div>
				<div>
					<label for="user-metadata" class="block text-sm font-medium text-gray-700 mb-1">Metadata <span class="text-gray-400 font-normal">(optional)</span></label>
					<textarea id="user-metadata" bind:value={createMetadata} placeholder={'{"role": "admin", "company": "Acme"}'} rows="3"
						class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono text-gray-900 placeholder-gray-300 focus:border-eurobase-500 focus:outline-none"></textarea>
					<p class="mt-1 text-xs text-gray-400">JSON object for custom user data.</p>
				</div>
			</div>
			<div class="flex items-center justify-end gap-3 border-t border-gray-200 px-6 py-4">
				<button type="button" class="cursor-pointer rounded-lg border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors" onclick={() => (showCreate = false)}>Cancel</button>
				<button type="button" class="cursor-pointer rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 transition-colors disabled:opacity-50" disabled={!createEmail.trim() || createPassword.length < 8 || creating} onclick={handleCreate}>
					{creating ? 'Creating...' : 'Create User'}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Edit User Modal -->
{#if editUser}
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<button type="button" class="fixed inset-0 bg-black/50 cursor-default" onclick={() => (editUser = null)} tabindex="-1" aria-label="Close"></button>
		<div class="relative z-10 w-full max-w-md rounded-xl bg-white shadow-2xl">
			<div class="flex items-center justify-between border-b border-gray-200 px-6 py-4">
				<h2 class="text-lg font-semibold text-gray-900">Edit User</h2>
				<button type="button" class="cursor-pointer rounded-lg p-1.5 text-gray-400 hover:bg-gray-100 hover:text-gray-600" onclick={() => (editUser = null)} aria-label="Close">
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12" /></svg>
				</button>
			</div>
			<div class="px-6 py-5 space-y-4">
				{#if editError}
					<div class="flex items-start gap-2 rounded-lg border border-red-200 bg-red-50 px-4 py-3">
						<svg class="h-4 w-4 mt-0.5 shrink-0 text-red-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
						</svg>
						<p class="text-sm text-red-700">{editError}</p>
					</div>
				{/if}
				<div>
					<label for="edit-email" class="block text-sm font-medium text-gray-700 mb-1">Email</label>
					<input id="edit-email" type="email" bind:value={editEmail}
						class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 focus:border-eurobase-500 focus:outline-none" />
				</div>
				<div>
					<label for="edit-name" class="block text-sm font-medium text-gray-700 mb-1">Display Name</label>
					<input id="edit-name" type="text" bind:value={editDisplayName} placeholder="Optional"
						class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 placeholder-gray-300 focus:border-eurobase-500 focus:outline-none" />
				</div>
				<div>
					<label for="edit-metadata" class="block text-sm font-medium text-gray-700 mb-1">Metadata</label>
					<textarea id="edit-metadata" bind:value={editMetadata} rows="4"
						class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono text-gray-900 focus:border-eurobase-500 focus:outline-none"></textarea>
				</div>
			</div>
			<div class="flex items-center justify-end gap-3 border-t border-gray-200 px-6 py-4">
				<button type="button" class="cursor-pointer rounded-lg border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors" onclick={() => (editUser = null)}>Cancel</button>
				<button type="button" class="cursor-pointer rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 transition-colors disabled:opacity-50" disabled={saving} onclick={handleEdit}>
					{saving ? 'Saving...' : 'Save Changes'}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Reset Password Modal -->
{#if resetUser}
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<button type="button" class="fixed inset-0 bg-black/50 cursor-default" onclick={() => (resetUser = null)} tabindex="-1" aria-label="Close"></button>
		<div class="relative z-10 w-full max-w-sm rounded-xl bg-white shadow-2xl">
			<div class="flex items-center justify-between border-b border-gray-200 px-6 py-4">
				<h2 class="text-lg font-semibold text-gray-900">Reset Password</h2>
				<button type="button" class="cursor-pointer rounded-lg p-1.5 text-gray-400 hover:bg-gray-100 hover:text-gray-600" onclick={() => (resetUser = null)} aria-label="Close">
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12" /></svg>
				</button>
			</div>
			<div class="px-6 py-5 space-y-4">
				<p class="text-sm text-gray-500">Set a new password for <strong>{resetUser.email}</strong>. All existing sessions will be revoked.</p>
				{#if resetError}
					<div class="flex items-start gap-2 rounded-lg border border-red-200 bg-red-50 px-4 py-3">
						<svg class="h-4 w-4 mt-0.5 shrink-0 text-red-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
						</svg>
						<p class="text-sm text-red-700">{resetError}</p>
					</div>
				{/if}
				<div>
					<label for="reset-password" class="block text-sm font-medium text-gray-700 mb-1">New Password</label>
					<input id="reset-password" type="password" bind:value={resetPassword} placeholder="Minimum 8 characters"
						class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 placeholder-gray-300 focus:border-eurobase-500 focus:outline-none" />
				</div>
				<div>
					<label for="reset-password-confirm" class="block text-sm font-medium text-gray-700 mb-1">Confirm Password</label>
					<input id="reset-password-confirm" type="password" bind:value={resetConfirmPassword} placeholder="Re-enter password"
						class="w-full rounded-lg border {resetPasswordsMismatch ? 'border-red-300' : 'border-gray-300'} px-3 py-2 text-sm text-gray-900 placeholder-gray-300 focus:border-eurobase-500 focus:outline-none" />
					{#if resetPasswordsMismatch}
						<p class="mt-1 text-xs text-red-500">Passwords do not match</p>
					{/if}
				</div>
			</div>
			<div class="flex items-center justify-end gap-3 border-t border-gray-200 px-6 py-4">
				<button type="button" class="cursor-pointer rounded-lg border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors" onclick={() => (resetUser = null)}>Cancel</button>
				<button type="button" class="cursor-pointer rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 transition-colors disabled:opacity-50" disabled={resetPassword.length < 8 || resetPassword !== resetConfirmPassword || resetting} onclick={handleResetPassword}>
					{resetting ? 'Resetting...' : 'Reset Password'}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Delete Confirm -->
{#if deleteConfirmUser}
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<button type="button" class="fixed inset-0 bg-black/50 cursor-default" onclick={() => (deleteConfirmUser = null)} tabindex="-1" aria-label="Close"></button>
		<div class="relative z-10 w-full max-w-sm rounded-xl bg-white shadow-2xl p-6">
			<div class="flex items-center gap-3 mb-4">
				<div class="flex h-10 w-10 items-center justify-center rounded-full bg-red-100">
					<svg class="h-5 w-5 text-red-600" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z" />
					</svg>
				</div>
				<div>
					<h3 class="text-sm font-semibold text-gray-900">Delete User</h3>
					<p class="text-xs text-gray-500">This will permanently remove <strong>{deleteConfirmUser.email}</strong> and revoke all their sessions.</p>
				</div>
			</div>
			<div class="flex justify-end gap-3">
				<button type="button" class="cursor-pointer rounded-lg border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors" onclick={() => (deleteConfirmUser = null)}>Cancel</button>
				<button type="button" class="cursor-pointer rounded-lg bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700 transition-colors disabled:opacity-50" disabled={deleting} onclick={handleDelete}>
					{deleting ? 'Deleting...' : 'Delete'}
				</button>
			</div>
		</div>
	</div>
{/if}
