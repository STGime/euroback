<script lang="ts">
	import { page } from '$app/stores';
	import { browser } from '$app/environment';
	import { getContext } from 'svelte';
	import { api, type ConnectInfo, type AuthConfig, type Project } from '$lib/api.js';
	import { lovableSkill, lovableSdkReference, appOrigin, lovableEntries } from '$lib/lovable';

	type IdeTab = 'claude' | 'lovable' | 'codex' | 'cursor' | 'windsurf' | 'generic';
	const STORAGE_KEY = 'eurobase:connect-tab';

	function loadSavedTab(): IdeTab {
		if (browser) {
			const saved = localStorage.getItem(STORAGE_KEY);
			if (saved === 'claude' || saved === 'lovable' || saved === 'codex' || saved === 'cursor' || saved === 'windsurf' || saved === 'generic') return saved;
		}
		return 'claude';
	}

	let projectId = $derived($page.params.id);
	let info = $state<ConnectInfo | null>(null);
	let loading = $state(true);
	let error = $state<string | null>(null);
	let activeTab = $state<IdeTab>(loadSavedTab());
	let copiedField = $state('');

	function setTab(tab: IdeTab) {
		activeTab = tab;
		if (browser) localStorage.setItem(STORAGE_KEY, tab);
	}

	$effect(() => {
		loadConnect();
	});

	async function loadConnect() {
		loading = true;
		error = null;
		try {
			const hiddenTables = new Set(['users', 'refresh_tokens', 'storage_objects', 'email_tokens', 'vault_secrets', 'storage_shared_prefixes']);
			info = await api.getConnectInfo(projectId);
			info.tables = info.tables.filter(t => !hiddenTables.has(t.name));
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load connect info';
		} finally {
			loading = false;
		}
	}

	async function copyToClipboard(text: string, field: string) {
		try {
			await navigator.clipboard.writeText(text);
			copiedField = field;
			setTimeout(() => { copiedField = ''; }, 2000);
		} catch {
			// silently fail
		}
	}

	// ── Lovable tab ──
	const projectCtx: { id: string; project: Project | null; updateProject: (p: Project) => void } | undefined = getContext('projectId');
	let publicKeyPrefix = $state('');
	let lovablePreview = $state('');
	let lovablePublished = $state('');
	let lovableCustom = $state('');
	let lovableSaving = $state(false);
	let lovableMessage = $state('');
	let lovableError = $state('');

	// The key hint belongs to one project: reset it when the project changes.
	let keyPrefixFor = '';
	$effect(() => {
		if (projectId !== keyPrefixFor) {
			publicKeyPrefix = '';
			keyPrefixFor = projectId ?? '';
		}
		if (activeTab === 'lovable' && !publicKeyPrefix && projectId) {
			api.listAPIKeys(projectId)
				.then((keys) => { publicKeyPrefix = keys.find((k) => k.type === 'public')?.key_prefix ?? ''; })
				.catch(() => { /* optional hint */ });
		}
	});

	let lovableEnv = $derived(info ? `VITE_EUROBASE_URL=${info.api_url}\nVITE_EUROBASE_PROJECT_ID=${info.project_id}\nVITE_EUROBASE_PUBLIC_KEY=eb_pk_…  # your project's public key` : '');
	let lovablePrompt = $derived(info ? `Use the /eurobase skill. This app uses Eurobase as its backend, not Lovable Cloud or Supabase. Put these in .env: VITE_EUROBASE_URL=${info.api_url}, VITE_EUROBASE_PROJECT_ID=${info.project_id}, VITE_EUROBASE_PUBLIC_KEY=eb_pk_…. Build …` : '');

	// The URLs typed in, as origins; invalid entries reported.
	let lovableOrigins = $derived.by(() => {
		const out: { label: string; raw: string; origin: string | null }[] = [];
		for (const [label, raw] of [['Preview URL', lovablePreview], ['Published URL', lovablePublished], ['Custom domain', lovableCustom]] as const) {
			if (raw.trim()) out.push({ label, raw, origin: appOrigin(raw) });
		}
		return out;
	});
	let lovablePlan = $derived.by(() => {
		const cfg = projectCtx?.project?.auth_config;
		const haveCors = new Set(cfg?.cors_origins ?? []);
		const haveRedirects = new Set(cfg?.redirect_urls ?? []);
		const cors: string[] = [];
		const redirects: string[] = [];
		for (const o of lovableOrigins) {
			if (!o.origin) continue;
			const e = lovableEntries(o.origin);
			for (const c of e.cors) if (!haveCors.has(c) && !cors.includes(c)) cors.push(c);
			for (const r of e.redirects) if (!haveRedirects.has(r) && !redirects.includes(r)) redirects.push(r);
		}
		return { cors, redirects };
	});

	async function addLovableUrls() {
		lovableMessage = '';
		lovableError = '';
		if (!projectCtx || !projectId) {
			lovableError = 'Project settings not loaded yet — try again in a moment.';
			return;
		}
		if (lovableOrigins.some((o) => !o.origin)) {
			lovableError = 'Fix the highlighted URLs first (https://… only).';
			return;
		}
		lovableSaving = true;
		try {
			// Merge into the settings as they are now, not as this page
			// loaded them (another tab or admin may have changed them).
			const fresh = await api.getProject(projectId);
			const existing: AuthConfig | undefined = fresh?.auth_config;
			if (!existing) throw new Error('Could not load the project settings.');
			projectCtx.updateProject(fresh);
			const haveCors = new Set(existing.cors_origins ?? []);
			const haveRedirects = new Set(existing.redirect_urls ?? []);
			const cors = lovablePlan.cors.filter((c) => !haveCors.has(c));
			const redirects = lovablePlan.redirects.filter((r) => !haveRedirects.has(r));
			if (cors.length === 0 && redirects.length === 0) {
				lovableMessage = 'Nothing to add — these URLs are already allowed.';
				return;
			}
			const merged: AuthConfig = {
				...existing,
				cors_origins: [...(existing.cors_origins ?? []), ...cors],
				redirect_urls: [...(existing.redirect_urls ?? []), ...redirects]
			};
			const updated = await api.updateProject(projectCtx.id, { auth_config: merged });
			if (updated) projectCtx.updateProject(updated);
			lovableMessage = `Added ${cors.length} CORS origin${cors.length === 1 ? '' : 's'} and ${redirects.length} redirect URL${redirects.length === 1 ? '' : 's'}.`;
		} catch (err) {
			lovableError = err instanceof Error ? err.message : 'Failed to save';
		} finally {
			lovableSaving = false;
		}
	}

	function downloadFile(filename: string, content: string) {
		const blob = new Blob([content], { type: 'text/plain' });
		const url = URL.createObjectURL(blob);
		const a = document.createElement('a');
		a.href = url;
		a.download = filename;
		a.click();
		URL.revokeObjectURL(url);
	}
</script>

<svelte:head>
	<title>Connect - Eurobase Console</title>
</svelte:head>

{#if loading}
	<div class="space-y-4">
		<div class="h-8 w-48 animate-pulse rounded bg-gray-200"></div>
		<div class="h-64 animate-pulse rounded-xl bg-gray-100"></div>
	</div>
{:else if error}
	<div class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">
		{error}
		<button onclick={loadConnect} class="ml-2 font-medium underline cursor-pointer">Retry</button>
	</div>
{:else if info}
	<div>
		<h2 class="text-lg font-bold text-gray-900">Connect your IDE</h2>
		<p class="mt-1 text-sm text-gray-500">
			Download configuration files pre-filled with your project's URL and schema. Drop them into your project directory.
		</p>

		<!-- IDE tabs -->
		<div class="mt-6 border-b border-gray-200">
			<nav class="flex gap-6 overflow-x-auto whitespace-nowrap" aria-label="IDE tabs">
				{#each [
					{ id: 'claude', label: 'Claude Code' },
					{ id: 'lovable', label: 'Lovable' },
					{ id: 'codex', label: 'Codex' },
					{ id: 'cursor', label: 'Cursor' },
					{ id: 'windsurf', label: 'Windsurf' },
					{ id: 'generic', label: 'Generic' }
				] as tab}
					<button
						onclick={() => setTab(tab.id as IdeTab)}
						class="border-b-2 pb-3 text-sm font-medium transition-colors cursor-pointer {activeTab === tab.id ? 'border-eurobase-600 text-eurobase-700' : 'border-transparent text-gray-500 hover:text-gray-700 hover:border-gray-300'}"
					>
						{tab.label}
					</button>
				{/each}
			</nav>
		</div>

		<div class="mt-6">
			{#if activeTab === 'claude'}
				<div class="space-y-4">
					<div class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
						<div class="flex items-center justify-between mb-3">
							<div>
								<p class="text-sm font-semibold text-gray-900">CLAUDE.md</p>
								<p class="text-xs text-gray-500">Drop this into your project root. Claude Code will use it for context.</p>
							</div>
							<div class="flex gap-2">
								<button
									onclick={() => copyToClipboard(info.claude_md, 'claude')}
									class="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors cursor-pointer"
								>
									{copiedField === 'claude' ? 'Copied!' : 'Copy'}
								</button>
								<button
									onclick={() => downloadFile('CLAUDE.md', info.claude_md)}
									class="rounded-md bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 transition-colors cursor-pointer"
								>
									Download
								</button>
							</div>
						</div>
						<pre class="rounded-lg bg-gray-50 border border-gray-100 p-4 text-xs font-mono text-gray-700 overflow-x-auto max-h-80 overflow-y-auto">{info.claude_md}</pre>
					</div>

					<div class="rounded-xl border border-eurobase-200 bg-eurobase-50/30 p-5 shadow-sm">
						<div class="flex items-center justify-between mb-3">
							<div>
								<p class="text-sm font-semibold text-gray-900">MCP Server</p>
								<p class="text-xs text-gray-500">Lets Claude Code list tables, run SQL, manage Vault, invoke functions on this project. Run once in your shell — paste a Personal Access Token from <a href="/account" class="text-eurobase-700 hover:underline">Account → Tokens</a>.</p>
							</div>
							<button
								onclick={() => copyToClipboard(info.mcp_config.claude, 'mcp-claude')}
								class="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors cursor-pointer"
							>
								{copiedField === 'mcp-claude' ? 'Copied!' : 'Copy'}
							</button>
						</div>
						<pre class="rounded-lg bg-gray-900 px-4 py-3 text-xs font-mono text-gray-100 overflow-x-auto">{info.mcp_config.claude}</pre>
						<p class="mt-3 text-xs text-gray-500">Or add to <code class="rounded bg-gray-100 px-1 font-mono">~/.claude/settings.json</code>:</p>
						<pre class="mt-2 rounded-lg bg-gray-50 border border-gray-100 p-4 text-xs font-mono text-gray-700 overflow-x-auto">{info.mcp_config.claude_json}</pre>
					</div>

					<div class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
						<div class="flex items-center justify-between mb-3">
							<div>
								<p class="text-sm font-semibold text-gray-900">.env</p>
								<p class="text-xs text-gray-500">Environment variables for your project</p>
							</div>
							<button
								onclick={() => downloadFile('.env', info.env_template)}
								class="rounded-md bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 transition-colors cursor-pointer"
							>
								Download
							</button>
						</div>
						<pre class="rounded-lg bg-gray-50 border border-gray-100 p-4 text-xs font-mono text-gray-700 overflow-x-auto">{info.env_template}</pre>
					</div>
				</div>

			{:else if activeTab === 'lovable'}
				<div class="space-y-4">
					<p class="text-sm text-gray-600">
						Build your Lovable app on this project instead of Lovable Cloud / Supabase. Your app talks to Eurobase directly
						from the browser with <code class="rounded bg-gray-100 px-1 text-xs">@eurobase/sdk</code>; the skill teaches Lovable how,
						and the connector lets it see and create your tables.
					</p>

					<!-- 1. Lovable Cloud -->
					<div class="rounded-xl border border-amber-200 bg-amber-50 p-5 shadow-sm">
						<p class="text-sm font-semibold text-gray-900">1. Stop Lovable from creating its own backend</p>
						<p class="mt-1 text-xs text-gray-700">
							In Lovable: <strong>Connectors → Cloud → Manage my agent's permissions</strong> → set <strong>Enable Cloud</strong> to
							<strong>Ask each time</strong>, and decline if it asks while you work on this app. Otherwise the first "add a login" prompt
							puts your users on Lovable Cloud (Supabase).
						</p>
					</div>

					<!-- 2. .env -->
					<div class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
						<div class="flex items-center justify-between mb-3">
							<div>
								<p class="text-sm font-semibold text-gray-900">2. Environment (.env in the Lovable project)</p>
								<p class="text-xs text-gray-500">
									Only the <strong>public</strong> key goes into Lovable{#if publicKeyPrefix} — yours starts with <code class="font-mono">{publicKeyPrefix}</code>{/if}.
									It was shown when the project was created; if you no longer have it, regenerate the keys in <a href="/p/{projectId}/settings" class="text-eurobase-700 hover:underline">Settings</a> (apps using the old key stop working).
									Never put a secret key (<code class="font-mono">eb_sk_…</code>) into Lovable.
								</p>
							</div>
							<button
								onclick={() => copyToClipboard(lovableEnv, 'lovable-env')}
								class="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors cursor-pointer"
							>{copiedField === 'lovable-env' ? 'Copied!' : 'Copy'}</button>
						</div>
						<pre class="rounded-lg bg-gray-50 border border-gray-100 p-4 text-xs font-mono text-gray-700 overflow-x-auto">{lovableEnv}</pre>
					</div>

					<!-- 3. Skill -->
					<div class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
						<div class="flex items-center justify-between mb-3">
							<div>
								<p class="text-sm font-semibold text-gray-900">3. The Eurobase skill</p>
								<p class="text-xs text-gray-500">
									Teaches Lovable the Eurobase SDK (it's not supabase-js), row-level security, file privacy and where secrets belong.
									Paste <code class="font-mono">SKILL.md</code> into <strong>Project settings → Knowledge</strong> in Lovable, or add it to your workspace's skills (owner / admin).
								</p>
							</div>
							<div class="flex gap-2 shrink-0">
								<button
									onclick={() => copyToClipboard(lovableSkill, 'lovable-skill')}
									class="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors cursor-pointer"
								>{copiedField === 'lovable-skill' ? 'Copied!' : 'Copy SKILL.md'}</button>
								<button
									onclick={() => downloadFile('SKILL.md', lovableSkill)}
									class="rounded-md bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 transition-colors cursor-pointer"
								>Download</button>
								<button
									onclick={() => downloadFile('sdk-reference.md', lovableSdkReference)}
									class="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors cursor-pointer"
								>SDK reference</button>
							</div>
						</div>
						<details class="text-xs text-gray-600">
							<summary class="cursor-pointer">Preview SKILL.md</summary>
							<pre class="mt-2 rounded-lg bg-gray-50 border border-gray-100 p-4 text-xs font-mono text-gray-700 overflow-x-auto max-h-80 overflow-y-auto">{lovableSkill}</pre>
						</details>
					</div>

					<!-- 4. MCP -->
					<div class="rounded-xl border border-eurobase-200 bg-eurobase-50/30 p-5 shadow-sm">
						<p class="text-sm font-semibold text-gray-900">4. Connect the Eurobase MCP server</p>
						<p class="mt-1 text-xs text-gray-600">
							In Lovable: <strong>Connectors → + → MCP server</strong>. Lets Lovable inspect and create tables here instead of guessing.
							The connection is personal to you and never part of your published app.
						</p>
						<dl class="mt-3 grid grid-cols-1 gap-2 text-xs sm:grid-cols-3">
							<div><dt class="text-gray-500">Server name</dt><dd class="font-mono text-gray-900">Eurobase</dd></div>
							<div>
								<dt class="text-gray-500">Server URL</dt>
								<dd class="flex items-center gap-2 font-mono text-gray-900">{info.mcp_url}
									<button onclick={() => copyToClipboard(info?.mcp_url ?? '', 'lovable-mcp')} class="text-eurobase-700 hover:underline cursor-pointer">{copiedField === 'lovable-mcp' ? 'Copied!' : 'Copy'}</button>
								</dd>
							</div>
							<div><dt class="text-gray-500">Authentication</dt><dd class="text-gray-900">Bearer token: a Personal Access Token from <a href="/account" class="text-eurobase-700 hover:underline">Account → Tokens</a></dd></div>
						</dl>
						<p class="mt-2 text-[11px] text-amber-800">
							A token can reach all your Eurobase projects: create a dedicated one named "lovable" with an expiry date.
						</p>
					</div>

					<!-- 5. Allowed URLs -->
					<div class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
						<p class="text-sm font-semibold text-gray-900">5. Allow your Lovable app's URLs</p>
						<p class="mt-1 text-xs text-gray-500">
							The browser only reaches Eurobase from approved origins, and sign-in emails and OAuth only return to approved URLs.
							Paste your app's URLs (from Lovable's preview and publish settings) — each is added to <strong>Allowed CORS origins</strong>
							and, with the skill's sign-in pages (<code class="font-mono">/verify</code>, <code class="font-mono">/reset-password</code>,
							<code class="font-mono">/magic</code>, <code class="font-mono">/auth/callback</code>), to <strong>Allowed redirect URLs</strong>
							(<a href="/p/{projectId}/auth" class="text-eurobase-700 hover:underline">Auth</a>).
							Wildcards like <code class="font-mono">*.lovable.app</code> aren't accepted, since anyone can publish there.
						</p>
						<div class="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-3">
							{#each [
								{ label: 'Preview URL', placeholder: 'https://id-preview--….lovable.app', bind: 'preview' },
								{ label: 'Published URL', placeholder: 'https://my-app.lovable.app', bind: 'published' },
								{ label: 'Custom domain (optional)', placeholder: 'https://app.example.com', bind: 'custom' }
							] as field}
								<label class="text-xs text-gray-700">{field.label}
									{#if field.bind === 'preview'}
										<input bind:value={lovablePreview} placeholder={field.placeholder} autocomplete="off" class="mt-1 block w-full rounded border px-2 py-1.5 font-mono text-xs {lovablePreview.trim() && !appOrigin(lovablePreview) ? 'border-red-400' : 'border-gray-300'}" />
									{:else if field.bind === 'published'}
										<input bind:value={lovablePublished} placeholder={field.placeholder} autocomplete="off" class="mt-1 block w-full rounded border px-2 py-1.5 font-mono text-xs {lovablePublished.trim() && !appOrigin(lovablePublished) ? 'border-red-400' : 'border-gray-300'}" />
									{:else}
										<input bind:value={lovableCustom} placeholder={field.placeholder} autocomplete="off" class="mt-1 block w-full rounded border px-2 py-1.5 font-mono text-xs {lovableCustom.trim() && !appOrigin(lovableCustom) ? 'border-red-400' : 'border-gray-300'}" />
									{/if}
								</label>
							{/each}
						</div>
						{#if lovablePlan.cors.length > 0 || lovablePlan.redirects.length > 0}
							<div class="mt-3 rounded-lg bg-gray-50 border border-gray-100 p-3 text-xs text-gray-700">
								<p class="font-medium">Will add:</p>
								{#if lovablePlan.cors.length > 0}<p class="mt-1">CORS origins: <span class="font-mono">{lovablePlan.cors.join(', ')}</span></p>{/if}
								{#if lovablePlan.redirects.length > 0}<p class="mt-1">Redirect URLs: <span class="font-mono break-all">{lovablePlan.redirects.join(', ')}</span></p>{/if}
							</div>
						{/if}
						<div class="mt-3 flex items-center gap-3">
							<button
								onclick={addLovableUrls}
								disabled={lovableSaving || lovableOrigins.length === 0}
								class="rounded-md bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 transition-colors cursor-pointer disabled:opacity-50"
							>{lovableSaving ? 'Saving…' : 'Add to allowed URLs'}</button>
							{#if lovableMessage}<span class="text-xs text-green-700">{lovableMessage}</span>{/if}
							{#if lovableError}<span class="text-xs text-red-600">{lovableError}</span>{/if}
						</div>
						<p class="mt-2 text-[11px] text-gray-400">Needs the admin role on this project. Remove entries later in Auth.</p>
					</div>

					<!-- 6. First prompt -->
					<div class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
						<div class="flex items-center justify-between mb-3">
							<div>
								<p class="text-sm font-semibold text-gray-900">6. Start building</p>
								<p class="text-xs text-gray-500">A first prompt that sets the ground rules — add your public key and what to build.</p>
							</div>
							<button
								onclick={() => copyToClipboard(lovablePrompt, 'lovable-prompt')}
								class="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors cursor-pointer"
							>{copiedField === 'lovable-prompt' ? 'Copied!' : 'Copy'}</button>
						</div>
						<pre class="rounded-lg bg-gray-50 border border-gray-100 p-4 text-xs font-mono text-gray-700 whitespace-pre-wrap">{lovablePrompt}</pre>
					</div>
				</div>
			{:else if activeTab === 'codex'}
				<div class="space-y-4">
					<div class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
						<div class="flex items-center justify-between mb-3">
							<div>
								<p class="text-sm font-semibold text-gray-900">AGENTS.md</p>
								<p class="text-xs text-gray-500">Drop this into your project root. Codex will use it for context.</p>
							</div>
							<div class="flex gap-2">
								<button
									onclick={() => copyToClipboard(info.codex_md, 'codex')}
									class="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors cursor-pointer"
								>
									{copiedField === 'codex' ? 'Copied!' : 'Copy'}
								</button>
								<button
									onclick={() => downloadFile('AGENTS.md', info.codex_md)}
									class="rounded-md bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 transition-colors cursor-pointer"
								>
									Download
								</button>
							</div>
						</div>
						<pre class="rounded-lg bg-gray-50 border border-gray-100 p-4 text-xs font-mono text-gray-700 overflow-x-auto max-h-80 overflow-y-auto">{info.codex_md}</pre>
					</div>

					<div class="rounded-xl border border-eurobase-200 bg-eurobase-50/30 p-5 shadow-sm">
						<div class="flex items-center justify-between mb-3">
							<div>
								<p class="text-sm font-semibold text-gray-900">MCP Server</p>
								<p class="text-xs text-gray-500">Append to <code class="rounded bg-gray-100 px-1 font-mono">~/.codex/config.toml</code> so Codex can run SQL, manage Vault, and invoke functions on this project.</p>
							</div>
							<button
								onclick={() => copyToClipboard(info.mcp_config.codex, 'mcp-codex')}
								class="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors cursor-pointer"
							>
								{copiedField === 'mcp-codex' ? 'Copied!' : 'Copy'}
							</button>
						</div>
						<pre class="rounded-lg bg-gray-900 px-4 py-3 text-xs font-mono text-gray-100 overflow-x-auto">{info.mcp_config.codex}</pre>
					</div>

					<div class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
						<div class="flex items-center justify-between mb-3">
							<div>
								<p class="text-sm font-semibold text-gray-900">.env</p>
								<p class="text-xs text-gray-500">Environment variables for your project</p>
							</div>
							<button
								onclick={() => downloadFile('.env', info.env_template)}
								class="rounded-md bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 transition-colors cursor-pointer"
							>
								Download
							</button>
						</div>
						<pre class="rounded-lg bg-gray-50 border border-gray-100 p-4 text-xs font-mono text-gray-700 overflow-x-auto">{info.env_template}</pre>
					</div>
				</div>

			{:else if activeTab === 'cursor'}
				<div class="space-y-4">
					<div class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
						<div class="flex items-center justify-between mb-3">
							<div>
								<p class="text-sm font-semibold text-gray-900">.cursorrules</p>
								<p class="text-xs text-gray-500">Cursor IDE context rules for your Eurobase project</p>
							</div>
							<div class="flex gap-2">
								<button
									onclick={() => copyToClipboard(info.cursor_rules, 'cursor')}
									class="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors cursor-pointer"
								>
									{copiedField === 'cursor' ? 'Copied!' : 'Copy'}
								</button>
								<button
									onclick={() => downloadFile('.cursorrules', info.cursor_rules)}
									class="rounded-md bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 transition-colors cursor-pointer"
								>
									Download
								</button>
							</div>
						</div>
						<pre class="rounded-lg bg-gray-50 border border-gray-100 p-4 text-xs font-mono text-gray-700 overflow-x-auto max-h-80 overflow-y-auto">{info.cursor_rules}</pre>
					</div>

					<div class="rounded-xl border border-eurobase-200 bg-eurobase-50/30 p-5 shadow-sm">
						<div class="flex items-center justify-between mb-3">
							<div>
								<p class="text-sm font-semibold text-gray-900">MCP Server</p>
								<p class="text-xs text-gray-500">Add to <code class="rounded bg-gray-100 px-1 font-mono">~/.cursor/mcp.json</code> (or your workspace's <code class="rounded bg-gray-100 px-1 font-mono">.cursor/mcp.json</code>) — paste a Personal Access Token from <a href="/account" class="text-eurobase-700 hover:underline">Account → Tokens</a>.</p>
							</div>
							<button
								onclick={() => copyToClipboard(info.mcp_config.cursor, 'mcp-cursor')}
								class="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors cursor-pointer"
							>
								{copiedField === 'mcp-cursor' ? 'Copied!' : 'Copy'}
							</button>
						</div>
						<pre class="rounded-lg bg-gray-900 px-4 py-3 text-xs font-mono text-gray-100 overflow-x-auto">{info.mcp_config.cursor}</pre>
					</div>

					<div class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
						<div class="flex items-center justify-between mb-3">
							<div>
								<p class="text-sm font-semibold text-gray-900">.env</p>
								<p class="text-xs text-gray-500">Environment variables for your project</p>
							</div>
							<button
								onclick={() => downloadFile('.env', info.env_template)}
								class="rounded-md bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 transition-colors cursor-pointer"
							>
								Download
							</button>
						</div>
						<pre class="rounded-lg bg-gray-50 border border-gray-100 p-4 text-xs font-mono text-gray-700 overflow-x-auto">{info.env_template}</pre>
					</div>
				</div>

			{:else if activeTab === 'windsurf'}
				<div class="space-y-4">
					<div class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
						<div class="flex items-center justify-between mb-3">
							<div>
								<p class="text-sm font-semibold text-gray-900">.windsurfrules</p>
								<p class="text-xs text-gray-500">Windsurf context rules (same format as Cursor)</p>
							</div>
							<div class="flex gap-2">
								<button
									onclick={() => copyToClipboard(info.cursor_rules, 'windsurf')}
									class="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors cursor-pointer"
								>
									{copiedField === 'windsurf' ? 'Copied!' : 'Copy'}
								</button>
								<button
									onclick={() => downloadFile('.windsurfrules', info.cursor_rules)}
									class="rounded-md bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 transition-colors cursor-pointer"
								>
									Download
								</button>
							</div>
						</div>
						<pre class="rounded-lg bg-gray-50 border border-gray-100 p-4 text-xs font-mono text-gray-700 overflow-x-auto max-h-80 overflow-y-auto">{info.cursor_rules}</pre>
					</div>

					<div class="rounded-xl border border-eurobase-200 bg-eurobase-50/30 p-5 shadow-sm">
						<div class="flex items-center justify-between mb-3">
							<div>
								<p class="text-sm font-semibold text-gray-900">MCP Server</p>
								<p class="text-xs text-gray-500">Add to Windsurf's MCP config — paste a Personal Access Token from <a href="/account" class="text-eurobase-700 hover:underline">Account → Tokens</a>.</p>
							</div>
							<button
								onclick={() => copyToClipboard(info.mcp_config.windsurf, 'mcp-windsurf')}
								class="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors cursor-pointer"
							>
								{copiedField === 'mcp-windsurf' ? 'Copied!' : 'Copy'}
							</button>
						</div>
						<pre class="rounded-lg bg-gray-900 px-4 py-3 text-xs font-mono text-gray-100 overflow-x-auto">{info.mcp_config.windsurf}</pre>
					</div>
				</div>

			{:else if activeTab === 'generic'}
				<div class="space-y-4">
					<!-- Install SDK -->
					<div class="rounded-xl border border-eurobase-200 bg-eurobase-50/30 p-5 shadow-sm">
						<div class="flex items-center justify-between mb-3">
							<div>
								<p class="text-sm font-semibold text-gray-900">1. Install the SDK</p>
								<p class="text-xs text-gray-500">Add the Eurobase JavaScript SDK to your project</p>
							</div>
							<button
								onclick={() => copyToClipboard('npm install @eurobase/sdk', 'install')}
								class="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors cursor-pointer"
							>
								{copiedField === 'install' ? 'Copied!' : 'Copy'}
							</button>
						</div>
						<pre class="rounded-lg bg-gray-900 px-4 py-3 text-sm font-mono text-gray-100 overflow-x-auto">npm install @eurobase/sdk</pre>
					</div>

					<!-- .env setup -->
					<div class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
						<div class="flex items-center justify-between mb-3">
							<div>
								<p class="text-sm font-semibold text-gray-900">2. Set up environment variables</p>
								<p class="text-xs text-gray-500">Create a <code class="text-xs bg-gray-100 rounded px-1">.env</code> file in your project root. Get your API keys from <a href="/p/{info.project_id}/settings" class="text-eurobase-600 hover:underline">Settings</a>.</p>
							</div>
							<button
								onclick={() => downloadFile('.env', info.env_template)}
								class="rounded-md bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 transition-colors cursor-pointer"
							>
								Download .env
							</button>
						</div>
						<pre class="rounded-lg bg-gray-900 px-4 py-3 text-xs font-mono text-gray-100 overflow-x-auto">{info.env_template}</pre>
					</div>

					<!-- Connection string -->
					<div class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
						<div class="flex items-center justify-between mb-3">
							<div>
								<p class="text-sm font-semibold text-gray-900">Connection String</p>
								<p class="text-xs text-gray-500">Alternative: pass a single URL instead of separate url + apiKey</p>
							</div>
						</div>
						<code class="block rounded-lg bg-gray-900 px-4 py-3 text-sm font-mono text-gray-100 overflow-x-auto">eurobase://<span class="text-amber-400">YOUR_PUBLIC_KEY</span>@{info.slug}.eurobase.app</code>
					</div>

					<!-- MCP endpoint -->
					<div class="rounded-xl border border-eurobase-200 bg-eurobase-50/30 p-5 shadow-sm">
						<div class="flex items-center justify-between mb-3">
							<div>
								<p class="text-sm font-semibold text-gray-900">MCP Server (Streamable HTTP)</p>
								<p class="text-xs text-gray-500">Any MCP-compatible client can connect. Auth: <code class="rounded bg-gray-100 px-1 font-mono">Authorization: Bearer &lt;PAT&gt;</code> · <a href="/account" class="text-eurobase-700 hover:underline">get your token here</a></p>
							</div>
							<button
								onclick={() => copyToClipboard(info.mcp_url, 'mcp-url')}
								class="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors cursor-pointer"
							>
								{copiedField === 'mcp-url' ? 'Copied!' : 'Copy'}
							</button>
						</div>
						<code class="block rounded-lg bg-gray-900 px-4 py-3 text-sm font-mono text-gray-100 overflow-x-auto">{info.mcp_url}</code>
					</div>

					{#if info.sample_code.javascript}
						<div class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
							<div class="flex items-center justify-between mb-3">
								<p class="text-sm font-semibold text-gray-900">3. Use the SDK</p>
								<button
									onclick={() => copyToClipboard(info.sample_code.javascript, 'js')}
									class="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors cursor-pointer"
								>
									{copiedField === 'js' ? 'Copied!' : 'Copy'}
								</button>
							</div>
							<pre class="rounded-lg bg-gray-900 p-4 text-xs font-mono text-gray-100 overflow-x-auto max-h-60 overflow-y-auto">{info.sample_code.javascript}</pre>
						</div>
					{/if}

					{#if info.sample_code.curl}
						<div class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
							<div class="flex items-center justify-between mb-3">
								<p class="text-sm font-semibold text-gray-900">Or use cURL directly</p>
								<button
									onclick={() => copyToClipboard(info.sample_code.curl, 'curl')}
									class="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors cursor-pointer"
								>
									{copiedField === 'curl' ? 'Copied!' : 'Copy'}
								</button>
							</div>
							<pre class="rounded-lg bg-gray-900 p-4 text-xs font-mono text-gray-100 overflow-x-auto">{info.sample_code.curl}</pre>
						</div>
					{/if}
				</div>
			{/if}
		</div>

		<!-- Schema overview -->
		{#if info.tables.length > 0}
			<div class="mt-8">
				<h3 class="text-sm font-semibold text-gray-900">Available Tables</h3>
				<div class="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
					{#each info.tables as table}
						<div class="rounded-lg border border-gray-200 bg-white p-3">
							<p class="text-sm font-medium text-gray-900">{table.name}</p>
							<p class="mt-1 text-xs text-gray-500">{table.columns.length} columns</p>
							<div class="mt-2 space-y-0.5">
								{#each table.columns as col}
									<div class="flex items-center gap-2 text-xs">
										<span class="font-mono text-gray-700">{col.name}</span>
										<span class="text-gray-400">{col.data_type}</span>
									</div>
								{/each}
							</div>
						</div>
					{/each}
				</div>
			</div>
		{/if}
	</div>
{/if}
