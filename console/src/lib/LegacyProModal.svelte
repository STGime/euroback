<script lang="ts">
	// Legacy-Pro conversion modal — shown on every route inside
	// the app layout when the user owns at least one project
	// matching the "was Pro at billing-flip time, hasn't paid
	// yet" rule (plan='pro' && legacy_pro_grace_until != null).
	//
	// Dismissable per-session by default; becomes non-dismissable
	// when grace ≤ 3 days. Session dismissal uses sessionStorage
	// so a full logout/login re-shows it — that's on purpose,
	// this cohort needs the nudge.
	import { goto } from '$app/navigation';
	import { api, type Project } from '$lib/api.js';

	let { project }: { project: Project } = $props();

	// Dismiss state — per-project so a user with two legacy-Pro
	// projects can silence one and still see the other. $derived
	// on the key rather than `const` so Svelte 5 treats the
	// project.id read as reactive (silences the "captures initial
	// value" warning; harmless in practice because props don't
	// change across mount, but the warning is worth respecting).
	let dismissKey = $derived(`legacy-pro-modal-dismissed:${project.id}`);
	let dismissedThisSession = $state(false);
	$effect(() => {
		if (typeof window !== 'undefined') {
			dismissedThisSession = sessionStorage.getItem(dismissKey) === '1';
		}
	});

	// Compute days-left from the grace timestamp. Ceil so "23h
	// left" reads as "1 day left" — user-facing rounding
	// deliberately favours urgency over precision.
	let graceDaysLeft = $derived.by(() => {
		if (!project.legacy_pro_grace_until) return 999;
		const then = new Date(project.legacy_pro_grace_until).getTime();
		return Math.max(0, Math.ceil((then - Date.now()) / (24 * 60 * 60 * 1000)));
	});

	// Non-dismissable when ≤3 days. Business rationale: at that
	// point the user is one hour-tick away from downgrade; the
	// modal is the only reliable notification channel.
	let dismissable = $derived(graceDaysLeft > 3);
	let visible = $derived(!dismissedThisSession || !dismissable);

	function dismiss(): void {
		if (!dismissable) return;
		if (typeof window !== 'undefined') sessionStorage.setItem(dismissKey, '1');
		dismissedThisSession = true;
	}

	function goToBilling(): void {
		goto(`/p/${project.id}/billing?plan=pro`);
	}

	async function seeFreePlan(): Promise<void> {
		// Sends the user to the billing page for a more
		// considered decision — the backend doesn't yet expose
		// a dedicated user-initiated cancel endpoint (PR 8).
		// Button label deliberately reads "See Free plan"
		// rather than "Switch to Free" (PR 7 review): the
		// action is navigation, not downgrade, and the old
		// label implied an immediate switch.
		goto(`/p/${project.id}/billing`);
	}
</script>

{#if visible}
	<!-- Backdrop -->
	<div
		class="fixed inset-0 z-40 bg-black/40"
		role="presentation"
		onclick={dismiss}
	></div>

	<!-- Modal -->
	<div
		class="fixed inset-0 z-50 flex items-center justify-center px-4"
		role="dialog"
		aria-modal="true"
		aria-labelledby="legacy-pro-modal-title"
	>
		<div class="max-w-lg rounded-lg bg-white shadow-2xl">
			<div class="border-b border-gray-200 px-6 py-4">
				<h2 id="legacy-pro-modal-title" class="text-lg font-semibold text-gray-900">
					Eurobase Pro is now paid
				</h2>
			</div>
			<div class="px-6 py-5">
				<p class="text-sm text-gray-700">
					Your Pro project <strong>{project.name}</strong> was created during
					closed beta, when Pro was free. Public beta opens billing today —
					Pro is <strong>€19/mo per project</strong>.
				</p>
				<p class="mt-4 text-sm {graceDaysLeft <= 3 ? 'font-medium text-red-700' : 'text-gray-700'}">
					{#if graceDaysLeft === 0}
						Your grace period has expired. This project will be
						downgraded to Free on the next sweep (within the hour).
					{:else if graceDaysLeft === 1}
						<strong>1 day left</strong> to add a payment method before this project drops to Free.
					{:else}
						You have <strong>{graceDaysLeft} days</strong> to add a payment method before this project drops to Free.
					{/if}
				</p>
				<p class="mt-4 text-xs text-gray-500">
					On Free: 5,000 MAU · 512 MB storage · 2 GB bandwidth · auto-pauses after 30 days idle.
				</p>
			</div>
			<div class="flex flex-col-reverse gap-2 border-t border-gray-200 px-6 py-4 sm:flex-row sm:justify-end">
				{#if dismissable}
					<button
						type="button"
						onclick={dismiss}
						class="rounded-md border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50"
					>
						Remind me later
					</button>
				{/if}
				<button
					type="button"
					onclick={seeFreePlan}
					class="rounded-md border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50"
				>
					See Free plan
				</button>
				<button
					type="button"
					onclick={goToBilling}
					class="rounded-md bg-blue-600 px-4 py-2 text-sm font-medium text-white shadow-sm hover:bg-blue-700"
				>
					Add payment (€19/mo)
				</button>
			</div>
		</div>
	</div>
{/if}
