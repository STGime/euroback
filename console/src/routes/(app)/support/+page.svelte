<script lang="ts">
	import { onMount } from 'svelte';
	import { api, APIError } from '$lib/api.js';

	// Team-tier gate: same flag the backend enforces. Non-Team users
	// land on an upgrade nudge instead of a form they can't submit.
	let hasTeamBeta = $state<boolean>(false);
	let profileLoaded = $state<boolean>(false);

	// Form state
	let subject = $state('');
	let category = $state<'question' | 'bug' | 'billing' | 'feature' | 'other'>('question');
	let priority = $state<'normal' | 'urgent'>('normal');
	let message = $state('');
	let submitting = $state(false);
	let error = $state('');
	let successId = $state('');

	onMount(async () => {
		try {
			const profile = await api.getProfile();
			hasTeamBeta = profile.team_beta_access === true;
		} catch {
			// Silent; treat as non-Team.
		} finally {
			profileLoaded = true;
		}
	});

	async function handleSubmit(e: Event) {
		e.preventDefault();
		if (!subject.trim() || !message.trim()) return;
		submitting = true;
		error = '';
		successId = '';
		try {
			const res = await api.submitSupportRequest({
				subject: subject.trim(),
				category,
				priority,
				message: message.trim(),
			});
			successId = res.id;
			// Clear the form so a follow-up ticket is a blank slate.
			subject = '';
			message = '';
			category = 'question';
			priority = 'normal';
		} catch (err) {
			if (err instanceof APIError) {
				error = err.message;
			} else {
				error = err instanceof Error ? err.message : 'Failed to submit support request';
			}
		} finally {
			submitting = false;
		}
	}

	let charCount = $derived(message.length);
	let charLimit = 10000;
</script>

<svelte:head>
	<title>Support — Eurobase Console</title>
</svelte:head>

<div class="mx-auto max-w-3xl">
	<div>
		<h1 class="text-2xl font-bold text-gray-900">Priority support</h1>
		<p class="mt-1 text-sm text-gray-500">
			24-hour SLA on Team-tier plans. Tickets route directly to the Eurobase team via
			our operations channel; you'll receive a reply at the email on your account.
		</p>
	</div>

	{#if !profileLoaded}
		<div class="mt-16 flex flex-col items-center text-center">
			<svg class="h-10 w-10 animate-spin text-eurobase-600" fill="none" viewBox="0 0 24 24">
				<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
				<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
			</svg>
		</div>
	{:else if !hasTeamBeta}
		<div class="mt-8 rounded-xl border border-amber-200 bg-amber-50 p-6">
			<h2 class="text-lg font-semibold text-amber-900">Priority support is a Team-tier feature</h2>
			<p class="mt-2 text-sm text-amber-800">
				Upgrade to Team to file priority-support tickets (24-hour SLA), plus SSO, dedicated
				Postgres, and organizations. Free and Pro users can still reach us via the public
				<a href="https://eurobase.app/#contact" class="underline hover:no-underline">contact form on the marketing site</a>.
			</p>
			<a
				href="/pricing"
				class="mt-4 inline-flex items-center gap-2 rounded-lg bg-amber-600 px-4 py-2 text-sm font-semibold text-white shadow-sm hover:bg-amber-700 transition-colors"
			>
				See Team-tier pricing
			</a>
		</div>
	{:else}
		<section class="mt-6 rounded-xl border border-gray-200 bg-white p-6 shadow-sm">
			{#if successId}
				<div class="rounded-lg bg-emerald-50 border border-emerald-200 p-4 text-sm text-emerald-800">
					<p class="font-medium">Ticket received — reference <code class="rounded bg-white/60 px-1 py-0.5 text-[11px]">{successId.slice(0, 8)}</code>.</p>
					<p class="mt-1">We'll reply within 24 hours (or sooner for urgent tickets) to the email on your account. Open another ticket below if you need to.</p>
				</div>
				<div class="mt-4 border-t border-gray-200 pt-4"></div>
			{/if}

			{#if error}
				<div class="mb-4 rounded-lg bg-red-50 border border-red-200 p-3 text-sm text-red-700">
					{error}
				</div>
			{/if}

			<form onsubmit={handleSubmit} class="space-y-4">
				<div>
					<label for="subject" class="block text-sm font-medium text-gray-700">Subject</label>
					<input
						id="subject"
						type="text"
						bind:value={subject}
						required
						maxlength="200"
						placeholder="Short summary of the issue"
						class="mt-1 block w-full rounded-lg border border-gray-300 px-3.5 py-2.5 text-sm text-gray-900 shadow-sm placeholder:text-gray-400 focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
					/>
				</div>

				<div class="grid grid-cols-2 gap-4">
					<div>
						<label for="category" class="block text-sm font-medium text-gray-700">Category</label>
						<select
							id="category"
							bind:value={category}
							class="mt-1 block w-full rounded-lg border border-gray-300 px-3.5 py-2.5 text-sm text-gray-900 shadow-sm focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
						>
							<option value="question">Question</option>
							<option value="bug">Bug report</option>
							<option value="billing">Billing</option>
							<option value="feature">Feature request</option>
							<option value="other">Other</option>
						</select>
					</div>
					<div>
						<label for="priority" class="block text-sm font-medium text-gray-700">Priority</label>
						<select
							id="priority"
							bind:value={priority}
							class="mt-1 block w-full rounded-lg border border-gray-300 px-3.5 py-2.5 text-sm text-gray-900 shadow-sm focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors"
						>
							<option value="normal">Normal (24h SLA)</option>
							<option value="urgent">Urgent</option>
						</select>
					</div>
				</div>

				<div>
					<label for="message" class="block text-sm font-medium text-gray-700">Message</label>
					<textarea
						id="message"
						bind:value={message}
						required
						rows="10"
						maxlength={charLimit}
						placeholder="Describe what's happening, what you expected, and any steps to reproduce. Paste stack traces or queries directly — they go straight to our ops channel."
						class="mt-1 block w-full rounded-lg border border-gray-300 px-3.5 py-2.5 text-sm text-gray-900 shadow-sm placeholder:text-gray-400 focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none transition-colors font-mono"
					></textarea>
					<div class="mt-1 flex items-center justify-between text-xs text-gray-400">
						<span>Markdown / plain text — up to {charLimit.toLocaleString()} characters.</span>
						<span class:text-amber-600={charCount > charLimit - 500}>{charCount.toLocaleString()} / {charLimit.toLocaleString()}</span>
					</div>
				</div>

				<div class="flex justify-end pt-2">
					<button
						type="submit"
						disabled={submitting || !subject.trim() || !message.trim()}
						class="inline-flex items-center gap-2 rounded-lg bg-eurobase-600 px-5 py-2.5 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 focus:outline-none focus:ring-2 focus:ring-eurobase-600 focus:ring-offset-2 transition-colors disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
					>
						{submitting ? 'Sending…' : 'Send to Eurobase support'}
					</button>
				</div>
			</form>
		</section>

		<section class="mt-6 rounded-xl border border-gray-200 bg-gray-50 p-4">
			<h3 class="text-sm font-semibold text-gray-800">What we can help with</h3>
			<ul class="mt-2 text-sm text-gray-600 space-y-1">
				<li>• Provisioning problems (project stuck in <code class="rounded bg-white px-1 py-0.5 text-[11px]">provisioning</code>, DB connection errors, storage upload failures).</li>
				<li>• Billing questions (invoices, VAT, plan changes, refunds).</li>
				<li>• Bug reports — the more reproducible, the faster the fix.</li>
				<li>• Feature requests and general product questions.</li>
			</ul>
			<p class="mt-3 text-xs text-gray-500">
				For public-facing questions (non-account-specific), the
				<a href="https://eurobase.app/#contact" class="text-eurobase-600 hover:text-eurobase-700 underline">marketing contact form</a>
				remains open.
			</p>
		</section>
	{/if}
</div>
