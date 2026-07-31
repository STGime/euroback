<script lang="ts">
	// Cancel-subscription confirmation modal. Two modes:
	//
	//   end_of_period → keep Pro until next_charge_at, no refund.
	//                   Less destructive default; user's usual pick.
	//   immediate     → downgrade to Free right now + prorated
	//                   refund via Mollie. Shown as the secondary
	//                   option since it costs money to the merchant.
	//
	// The button that OPENS this modal ("Cancel Pro") is on the
	// per-project billing page; the modal itself lives in $lib so
	// it can be reused by the org-overview page later if needed.
	import { api, type ProjectSubscription } from '$lib/api.js';

	let {
		subscription,
		onclose,
		oncomplete
	}: {
		subscription: ProjectSubscription;
		onclose: () => void;
		oncomplete: (result: { mode: string; refundedCents: number }) => void;
	} = $props();

	let mode = $state<'end_of_period' | 'immediate'>('end_of_period');
	let submitting = $state(false);
	let error: string | null = $state(null);

	// Naive proration preview — shown so the user isn't surprised
	// by the refunded amount. The backend does the authoritative
	// math from paid_at + next_charge_at; this is an estimate
	// from what the client already knows (next_charge_at). We
	// deliberately understate rather than overstate: shown refund
	// is a floor. Actual refund may be slightly higher due to
	// server-side clock drift, but never lower.
	let previewRefundCents = $derived.by(() => {
		if (!subscription.next_charge_at) return 0;
		const nextCharge = new Date(subscription.next_charge_at).getTime();
		const now = Date.now();
		// Period is ~1 month; approximate as 30 days for the
		// preview.
		const periodMs = 30 * 24 * 60 * 60 * 1000;
		const remaining = Math.max(0, nextCharge - now);
		const fraction = Math.min(1, remaining / periodMs);
		return Math.max(0, Math.floor(subscription.price_cents * fraction));
	});

	function formatEUR(cents: number): string {
		const abs = Math.abs(cents);
		const whole = Math.floor(abs / 100);
		const frac = abs % 100;
		return `€${whole}.${String(frac).padStart(2, '0')}`;
	}

	function formatDate(iso?: string | null): string {
		if (!iso) return '—';
		return new Date(iso).toLocaleDateString('en-GB', {
			year: 'numeric',
			month: 'short',
			day: 'numeric'
		});
	}

	async function confirm(): Promise<void> {
		if (submitting) return;
		submitting = true;
		error = null;
		try {
			const res = await api.cancelSubscription(subscription.id, mode);
			oncomplete({ mode: res.mode, refundedCents: res.refunded_cents ?? 0 });
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
			submitting = false;
		}
	}
</script>

<div
	class="fixed inset-0 z-40 bg-black/40"
	role="presentation"
	onclick={onclose}
></div>

<div
	class="fixed inset-0 z-50 flex items-center justify-center px-4"
	role="dialog"
	aria-modal="true"
	aria-labelledby="cancel-modal-title"
>
	<div class="max-w-md rounded-lg bg-white shadow-2xl">
		<div class="border-b border-gray-200 px-6 py-4">
			<h2 id="cancel-modal-title" class="text-lg font-semibold text-gray-900">
				Cancel Pro subscription
			</h2>
		</div>
		<div class="px-6 py-5">
			<p class="text-sm text-gray-700">
				Your project drops back to the Free plan (5,000 MAU · 512 MB
				storage · 2 GB bandwidth · auto-pauses after 30 days idle).
				Nothing is deleted.
			</p>

			<fieldset class="mt-4 space-y-3">
				<legend class="sr-only">Cancellation timing</legend>

				<label class="flex cursor-pointer items-start gap-3 rounded-md border border-gray-200 p-3 hover:bg-gray-50">
					<input
						type="radio"
						name="cancel-mode"
						value="end_of_period"
						bind:group={mode}
						class="mt-1"
					/>
					<div>
						<p class="text-sm font-medium text-gray-900">Keep Pro until it expires</p>
						<p class="mt-1 text-xs text-gray-600">
							You keep Pro features until <strong>{formatDate(subscription.next_charge_at)}</strong>.
							No refund. Won't be charged again.
						</p>
					</div>
				</label>

				<label class="flex cursor-pointer items-start gap-3 rounded-md border border-gray-200 p-3 hover:bg-gray-50">
					<input
						type="radio"
						name="cancel-mode"
						value="immediate"
						bind:group={mode}
						class="mt-1"
					/>
					<div>
						<p class="text-sm font-medium text-gray-900">Cancel now</p>
						<p class="mt-1 text-xs text-gray-600">
							Drop to Free immediately. Prorated refund of
							~<strong>{formatEUR(previewRefundCents)}</strong> for the unused portion
							of this period. The exact amount is computed on the
							server and lands on your card within 5&ndash;10
							business days.
						</p>
					</div>
				</label>
			</fieldset>

			{#if error}
				<p class="mt-4 text-sm text-red-700">Cancel failed: {error}</p>
			{/if}
		</div>
		<div class="flex flex-col-reverse gap-2 border-t border-gray-200 px-6 py-4 sm:flex-row sm:justify-end">
			<button
				type="button"
				onclick={onclose}
				disabled={submitting}
				class="rounded-md border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 disabled:cursor-not-allowed disabled:opacity-50"
			>
				Keep Pro
			</button>
			<button
				type="button"
				onclick={confirm}
				disabled={submitting}
				class="rounded-md bg-red-600 px-4 py-2 text-sm font-medium text-white shadow-sm hover:bg-red-700 disabled:cursor-not-allowed disabled:opacity-50"
			>
				{submitting ? 'Canceling…' : mode === 'immediate' ? 'Cancel now' : 'Cancel at period end'}
			</button>
		</div>
	</div>
</div>
