<script lang="ts">
	// Floating "Contact us" button (bottom right) for signed-out pages,
	// the console's copy of the marketing site's widget. Posts to the
	// public /platform/public/contact endpoint, which rate-limits, owns the
	// strict email validation (field-specific 400s) and notifies ops.
	import { onMount, tick } from 'svelte';
	import { PUBLIC_API_URL } from '$env/static/public';

	const API_URL = `${PUBLIC_API_URL ?? '/api'}/platform/public/contact`;
	const MESSAGE_MAX = 5000;

	let open = $state(false);
	let submitting = $state(false);
	let submitted = $state(false);
	let errorMsg = $state<string | null>(null);

	let name = $state('');
	let email = $state('');
	let message = $state('');

	let emailInput = $state<HTMLInputElement | null>(null);
	let previouslyFocused: HTMLElement | null = null;
	let successTimer: ReturnType<typeof setTimeout> | null = null;

	function clearSuccessTimer() {
		if (successTimer !== null) {
			clearTimeout(successTimer);
			successTimer = null;
		}
	}

	async function openWidget() {
		previouslyFocused = (document.activeElement as HTMLElement) ?? null;
		open = true;
		errorMsg = null;
		document.body.style.overflow = 'hidden';
		await tick();
		emailInput?.focus();
	}

	function closeWidget() {
		open = false;
		clearSuccessTimer();
		document.body.style.overflow = '';
		previouslyFocused?.focus?.();
		previouslyFocused = null;
		// The draft stays, so reopening restores it; only a sent
		// message clears the form (resetForm).
	}

	function resetForm() {
		name = '';
		email = '';
		message = '';
		submitted = false;
		errorMsg = null;
		clearSuccessTimer();
	}

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		if (submitting) return;
		errorMsg = null;
		const trimmedEmail = email.trim();
		const trimmedMessage = message.trim();
		if (!trimmedEmail || !trimmedMessage) {
			errorMsg = 'Email and message are both required.';
			return;
		}
		submitting = true;
		try {
			const body: Record<string, string> = { email: trimmedEmail, message: trimmedMessage };
			const trimmedName = name.trim();
			if (trimmedName) body.name = trimmedName;
			const res = await fetch(API_URL, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify(body)
			});
			if (res.status === 201) {
				submitted = true;
				clearSuccessTimer();
				successTimer = setTimeout(() => {
					successTimer = null;
					resetForm();
					closeWidget();
				}, 2400);
				return;
			}
			let apiMsg = '';
			try {
				const j = await res.json();
				if (j?.error) apiMsg = String(j.error);
			} catch {
				/* not JSON */
			}
			if (res.status === 429) errorMsg = apiMsg || 'Too many messages — please try again later.';
			else if (res.status === 400) errorMsg = apiMsg || 'Please check the form and try again.';
			else errorMsg = apiMsg || 'Something went wrong. Please try again.';
		} catch {
			errorMsg = 'Network error — please check your connection and try again.';
		} finally {
			submitting = false;
		}
	}

	function onKeydown(e: KeyboardEvent) {
		// Escape during IME composition cancels the character, not the dialog.
		if (e.isComposing || e.keyCode === 229) return;
		if (e.key === 'Escape' && open) closeWidget();
	}

	onMount(() => {
		window.addEventListener('keydown', onKeydown);
		return () => {
			window.removeEventListener('keydown', onKeydown);
			clearSuccessTimer();
			if (open) document.body.style.overflow = '';
		};
	});
</script>

{#if !open}
	<button
		type="button"
		aria-label="Contact us"
		class="fixed bottom-6 right-6 z-40 inline-flex items-center gap-2 rounded-full bg-eurobase-600 px-5 py-3 text-sm font-semibold text-white shadow-lg shadow-gray-900/20 transition-colors hover:bg-eurobase-700 focus:outline-none focus-visible:ring-2 focus-visible:ring-eurobase-500 focus-visible:ring-offset-2"
		onclick={openWidget}
	>
		<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor" aria-hidden="true">
			<path stroke-linecap="round" stroke-linejoin="round" d="M8.625 12a.375.375 0 1 1-.75 0 .375.375 0 0 1 .75 0Zm0 0H8.25m4.125 0a.375.375 0 1 1-.75 0 .375.375 0 0 1 .75 0Zm0 0H12m4.125 0a.375.375 0 1 1-.75 0 .375.375 0 0 1 .75 0Zm0 0h-.375M21 12c0 4.556-4.03 8.25-9 8.25a9.764 9.764 0 0 1-2.555-.337A5.972 5.972 0 0 1 5.41 20.97a5.969 5.969 0 0 1-.474-.065 4.48 4.48 0 0 0 .978-2.025c.09-.457-.133-.901-.467-1.226C3.93 16.178 3 14.189 3 12c0-4.556 4.03-8.25 9-8.25s9 3.694 9 8.25Z" />
		</svg>
		Contact us
	</button>
{:else}
	<div
		class="fixed inset-0 z-50 flex items-end justify-end p-4 sm:items-center sm:p-6"
		role="dialog"
		aria-modal="true"
		aria-labelledby="contact-widget-title"
	>
		<button type="button" aria-label="Close" tabindex="-1" class="absolute inset-0 cursor-default bg-gray-900/40 backdrop-blur-sm" onclick={closeWidget}></button>

		<div class="relative w-full max-w-sm space-y-4 rounded-2xl border border-gray-200 bg-white p-5 shadow-2xl shadow-gray-900/10">
			<div class="flex items-start justify-between gap-3">
				<div>
					<h2 id="contact-widget-title" class="text-lg font-semibold text-gray-900">Send us a message</h2>
					<p class="mt-1 text-xs text-gray-500">Questions, feedback, procurement — we read every one.</p>
				</div>
				<button
					type="button"
					aria-label="Close"
					class="rounded-md p-1 text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-700"
					onclick={closeWidget}
				>
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" aria-hidden="true">
						<path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12" />
					</svg>
				</button>
			</div>

			{#if submitted}
				<div class="rounded-lg border border-green-200 bg-green-50 p-4 text-sm text-green-800" role="status">
					<p class="font-medium">Message sent — thanks.</p>
					<p class="mt-1 text-green-700">We'll get back to you at the email you provided.</p>
				</div>
			{:else}
				<form class="space-y-3" onsubmit={submit} novalidate>
					<div>
						<label for="cw-name" class="mb-1 block text-xs font-medium text-gray-600">
							Name <span class="font-normal text-gray-400">(optional)</span>
						</label>
						<input
							id="cw-name"
							bind:value={name}
							type="text"
							maxlength="200"
							autocomplete="name"
							class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-eurobase-500 focus:outline-none focus:ring-1 focus:ring-eurobase-500"
						/>
					</div>
					<div>
						<label for="cw-email" class="mb-1 block text-xs font-medium text-gray-600">Email</label>
						<input
							id="cw-email"
							bind:this={emailInput}
							bind:value={email}
							type="email"
							required
							maxlength="254"
							autocomplete="email"
							placeholder="you@company.eu"
							class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-eurobase-500 focus:outline-none focus:ring-1 focus:ring-eurobase-500"
						/>
					</div>
					<div>
						<label for="cw-message" class="mb-1 block text-xs font-medium text-gray-600">Message</label>
						<textarea
							id="cw-message"
							bind:value={message}
							required
							rows="4"
							maxlength={MESSAGE_MAX}
							placeholder="What's on your mind?"
							class="w-full resize-none rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-eurobase-500 focus:outline-none focus:ring-1 focus:ring-eurobase-500"
						></textarea>
						<p class="mt-1 text-right text-[10px] text-gray-400">{message.length} / {MESSAGE_MAX}</p>
					</div>

					{#if errorMsg}
						<p class="text-xs text-red-600" role="alert">{errorMsg}</p>
					{/if}

					<div class="flex items-center justify-between gap-3 pt-1">
						<p class="text-[10px] leading-tight text-gray-500">
							By sending you accept our
							<a href="/legal/privacy" target="_blank" rel="noopener noreferrer" class="underline hover:text-gray-900">privacy notice</a>.
						</p>
						<button
							type="submit"
							disabled={submitting}
							class="inline-flex items-center gap-2 rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-eurobase-700 disabled:cursor-not-allowed disabled:opacity-50 focus:outline-none focus-visible:ring-2 focus-visible:ring-eurobase-500 focus-visible:ring-offset-2"
						>
							{#if submitting}
								<svg class="h-4 w-4 animate-spin" fill="none" viewBox="0 0 24 24" aria-hidden="true">
									<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
									<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
								</svg>
							{/if}
							{submitting ? 'Sending…' : 'Send'}
						</button>
					</div>
				</form>
			{/if}
		</div>
	</div>
{/if}
