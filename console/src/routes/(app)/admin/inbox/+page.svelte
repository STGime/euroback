<script lang="ts">
	import { api, type InboxItem } from '$lib/api.js';

	// Superadmin inbox: Team-tier support tickets and contact / Team-beta
	// requests, in full (Discord only says that one arrived). Mark a message
	// resolved with a short note once it's handled; reopen if needed.
	type Status = 'open' | 'resolved' | 'all';

	let status = $state<Status>('open');
	let items: InboxItem[] = $state([]);
	let openCount = $state(0);
	let limit = $state(200);
	let loading = $state(true);
	let error: string | null = $state(null);
	let notes = $state<Record<string, string>>({});
	let busy = $state<string | null>(null);
	let requestSeq = 0;

	const key = (it: InboxItem) => `${it.kind}:${it.id}`;

	// The spinner only on a fresh load (filter change); after resolve /
	// reopen the list refreshes in place, keeping scroll position and notes.
	async function load(quiet = false) {
		const seq = ++requestSeq;
		if (!quiet) loading = true;
		error = null;
		try {
			const resp = await api.adminInbox(status);
			if (seq !== requestSeq) return;
			items = resp.items;
			openCount = resp.open;
			limit = resp.limit;
		} catch (e) {
			if (seq !== requestSeq) return;
			error = e instanceof Error ? e.message : 'Failed to load the inbox';
		} finally {
			if (seq === requestSeq) loading = false;
		}
	}

	$effect(() => {
		void status;
		load();
	});

	async function resolve(it: InboxItem) {
		busy = key(it);
		error = null;
		try {
			await api.adminResolveInboxItem(it.kind, it.id, (notes[key(it)] ?? '').trim());
			await load(true);
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to update the message';
		} finally {
			busy = null;
		}
	}

	async function reopen(it: InboxItem) {
		busy = key(it);
		error = null;
		try {
			await api.adminReopenInboxItem(it.kind, it.id);
			await load(true);
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to update the message';
		} finally {
			busy = null;
		}
	}

	function title(it: InboxItem): string {
		if (it.kind === 'support') return it.subject || '(no subject)';
		if (it.source === 'team_beta_request') return 'Team beta request';
		if (it.source === 'console') return 'Contact form (console sign-in)';
		return 'Contact form';
	}

	function replyLink(it: InboxItem): string {
		const subject = it.kind === 'support' && it.subject ? `Re: ${it.subject}` : 'Re: your message to Eurobase';
		return `mailto:${encodeURIComponent(it.email)}?subject=${encodeURIComponent(subject)}`;
	}

	// Reply template for Team beta requests: questions that tell an
	// evaluation we can support from one we can't, before a dedicated
	// database is provisioned for it. Plain text (mailto body).
	const BETA_QUESTIONS = `Hello,

thanks for your interest in the Eurobase Team beta. Every beta project gets its own dedicated PostgreSQL instance, so we admit a limited number of teams and ask a few questions first:

1. Company: name, website, and your role.
2. Current setup: which Supabase plan, database size (GB), and which features you use (Auth, Storage, Edge Functions, Realtime, RLS-heavy schemas, extensions).
3. Scale: number of end users or monthly active users, and rough requests per day.
4. Team: how many people need console access, and roles (developers, admins, read-only).
5. SSO: which identity provider (Microsoft Entra ID, Google Workspace, Okta, Keycloak, ...)? Eurobase supports SSO via OIDC; SAML is not available.
6. Data residency: which requirements apply (GDPR, sector rules, customer contracts), and who does the review?
7. Timeline: when do you plan to start the migration, and when would production go live?
8. After the beta: Team costs EUR 149 per month per organisation, including one dedicated database. Is that within your budget?
9. Feedback: would you have 20 minutes for a call during the evaluation?

Please send your answers to contact@eurobase.app from your work email address. The beta covers 14 days from the creation of your Team project. Once we have your answers we'll get back to you within two working days.

Best regards,
Eurobase`;

	function betaQuestionsLink(it: InboxItem): string {
		return `mailto:${encodeURIComponent(it.email)}?subject=${encodeURIComponent('Re: Eurobase Team beta — a few questions')}&body=${encodeURIComponent(BETA_QUESTIONS)}`;
	}

	let copied = $state<string | null>(null);
	async function copyBetaQuestions(it: InboxItem) {
		try {
			await navigator.clipboard.writeText(BETA_QUESTIONS);
			copied = key(it);
			setTimeout(() => (copied = null), 2000);
		} catch {
			/* clipboard unavailable: the mailto link still works */
		}
	}

	function when(ts: string): string {
		return new Date(ts).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
	}
</script>

<div class="p-6 max-w-5xl space-y-6">
	<header class="flex items-start justify-between gap-4">
		<div>
			<a href="/admin" class="text-sm text-blue-600 hover:underline">← Platform Admin</a>
			<h1 class="mt-1 text-2xl font-semibold text-gray-900">Inbox</h1>
			<p class="text-sm text-gray-500 mt-1">
				Team-tier support tickets and contact / Team-beta requests, in full. {openCount} open{#if items.length >= limit}
					· showing the newest {items.length}{/if}.
			</p>
		</div>
		<div class="flex rounded-lg border border-gray-200 bg-white text-sm" role="tablist" aria-label="Filter">
			{#each ['open', 'resolved', 'all'] as s (s)}
				<button
					type="button"
					role="tab"
					aria-selected={status === s}
					onclick={() => (status = s as Status)}
					class="px-3 py-1.5 capitalize cursor-pointer {status === s ? 'bg-eurobase-50 text-eurobase-700 font-medium' : 'text-gray-600 hover:bg-gray-50'}"
				>
					{s}
				</button>
			{/each}
		</div>
	</header>

	{#if error}
		<div class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">{error}</div>
	{/if}

	{#if loading}
		<div class="flex items-center gap-2 text-sm text-gray-500" role="status">
			<svg class="h-5 w-5 animate-spin text-eurobase-600" fill="none" viewBox="0 0 24 24" aria-hidden="true">
				<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
				<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
			</svg>
			Loading messages…
		</div>
	{:else if items.length === 0}
		<p class="text-sm text-gray-500">No {status === 'all' ? '' : status} messages.</p>
	{:else}
		<ul class="space-y-4">
			{#each items as it (key(it))}
				<li class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm space-y-3">
					<div class="flex flex-wrap items-center gap-2 text-xs">
						<span class="rounded px-2 py-0.5 font-medium {it.kind === 'support' ? 'bg-violet-100 text-violet-700' : 'bg-sky-100 text-sky-700'}">
							{it.kind === 'support' ? 'Support' : 'Contact'}
						</span>
						{#if it.category}<span class="rounded bg-gray-100 px-2 py-0.5 text-gray-600">{it.category}</span>{/if}
						{#if it.priority && it.priority !== 'normal'}<span class="rounded bg-amber-100 px-2 py-0.5 text-amber-700">{it.priority}</span>{/if}
						{#if it.project_slug}<span class="rounded bg-gray-100 px-2 py-0.5 font-mono text-gray-600">{it.project_slug}</span>{/if}
						<span class="text-gray-400">{when(it.created_at)}</span>
						{#if it.resolved_at}
							<span class="rounded bg-emerald-100 px-2 py-0.5 text-emerald-700">
								resolved {when(it.resolved_at)}{it.resolved_by_email ? ` by ${it.resolved_by_email}` : ''}
							</span>
						{/if}
					</div>

					<div>
						<h2 class="text-base font-semibold text-gray-900">{title(it)}</h2>
						<p class="text-sm text-gray-500">
							From {it.name ? `${it.name} · ` : ''}<span class="font-mono">{it.email}</span>
						</p>
					</div>

					<p class="whitespace-pre-wrap break-words text-sm text-gray-800 leading-relaxed">{it.message}</p>

					{#if it.resolution_note}
						<p class="rounded-lg bg-gray-50 px-3 py-2 text-xs text-gray-600"><span class="font-medium">Note:</span> {it.resolution_note}</p>
					{/if}

					<div class="flex flex-wrap items-end gap-2 border-t border-gray-100 pt-3">
						<a href={replyLink(it)} class="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-700 hover:bg-gray-50">
							Reply by email
						</a>
						{#if it.source === 'team_beta_request'}
							<a href={betaQuestionsLink(it)} class="rounded-md border border-eurobase-200 bg-eurobase-50 px-3 py-1.5 text-xs font-medium text-eurobase-700 hover:bg-eurobase-100">
								Reply with beta questions
							</a>
							<button
								type="button"
								onclick={() => copyBetaQuestions(it)}
								class="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-700 hover:bg-gray-50 cursor-pointer"
							>
								{copied === key(it) ? 'Copied' : 'Copy questions'}
							</button>
						{/if}
						{#if it.resolved_at}
							<button
								type="button"
								onclick={() => reopen(it)}
								disabled={busy === key(it)}
								class="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-50 cursor-pointer"
							>
								Reopen
							</button>
						{:else}
							<label class="flex-1 min-w-[12rem]">
								<span class="sr-only">Note (optional)</span>
								<input
									type="text"
									maxlength="2000"
									placeholder={it.resolution_note ? 'Keep the note above, or write a new one' : 'Note (optional), e.g. answered by email'}
									bind:value={notes[key(it)]}
									class="w-full rounded-md border border-gray-200 px-3 py-1.5 text-xs text-gray-800 focus:border-eurobase-500 focus:outline-none"
								/>
							</label>
							<button
								type="button"
								onclick={() => resolve(it)}
								disabled={busy === key(it)}
								class="rounded-md bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 disabled:opacity-50 cursor-pointer"
							>
								Mark resolved
							</button>
						{/if}
					</div>
				</li>
			{/each}
		</ul>
	{/if}
</div>
