<script lang="ts">
	// Cancel-subscription confirmation modal. Single mode:
	// end_of_period — keep Pro until next_charge_at, no refund.
	//
	// Policy update 2026-08-16: prorated-refund cancellation was
	// dropped from the product. Users always keep Pro until the
	// end of the current billing period. The backend service
	// still has the immediate-cancel-with-refund code path but
	// the handler rejects it with 400, so this modal only ever
	// requests end_of_period.
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

	let submitting = $state(false);
	let error: string | null = $state(null);

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
			const res = await api.cancelSubscription(subscription.id, 'end_of_period');
			oncomplete({ mode: res.mode, refundedCents: 0 });
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
				You'll keep Pro features until
				<strong>{formatDate(subscription.next_charge_at)}</strong> — no
				further charges after this billing cycle. On that date the
				project drops back to Free (5,000 MAU · 512 MB storage · 2 GB
				bandwidth · auto-pauses after 30 days idle). Nothing is
				deleted.
			</p>

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
				{submitting ? 'Canceling…' : 'Cancel at period end'}
			</button>
		</div>
	</div>
</div>
