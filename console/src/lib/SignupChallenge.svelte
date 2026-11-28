<script lang="ts">
	// Signup proof-of-work check (ALTCHA, self-hosted). The widget fetches a
	// challenge from the gateway and solves it in Web Workers while the user
	// fills in the form; nothing goes to a third party. `payload` is the
	// solution to send with the signup (null until solved, or after it
	// expired).
	import { onMount } from 'svelte';
	import { api } from '$lib/api.js';

	let { payload = $bindable<string | null>(null) }: { payload?: string | null } = $props();

	type Widget = HTMLElement & {
		configure: (c: Record<string, unknown>) => Promise<void>;
		reset: () => void;
		verify: () => Promise<{ payload: string } | null>;
	};
	let el = $state<Widget | null>(null);
	let ready = $state(false);
	// A solve we started (refresh), and one the widget started by itself
	// (auto on load), so submit can wait for either instead of restarting.
	let solving: Promise<string | null> | null = null;
	let widgetRun: { promise: Promise<string | null>; resolve: (p: string | null) => void } | null = null;
	let errorRetryTimer: ReturnType<typeof setTimeout> | undefined;
	const SOLVE_TIMEOUT_MS = 60_000;

	function withTimeout(p: Promise<string | null>): Promise<string | null> {
		return Promise.race([p, new Promise<null>((r) => setTimeout(() => r(null), SOLVE_TIMEOUT_MS))]);
	}

	onMount(() => {
		let cancelled = false;
		const onState = (ev: Event) => {
			const d = (ev as CustomEvent<{ state: string; payload?: string }>).detail;
			const state = d?.state;
			if (state === 'verifying') {
				if (!solving && !widgetRun) {
					let resolve!: (p: string | null) => void;
					const promise = new Promise<string | null>((r) => (resolve = r));
					widgetRun = { promise, resolve };
				}
				return;
			}
			// While our own solve runs, the widget reports the run that
			// reset() aborted as an error: not a real failure.
			if (state === 'error' && solving) return;
			payload = state === 'verified' && d.payload ? d.payload : null;
			widgetRun?.resolve(payload);
			widgetRun = null;
			// The widget doesn't start over by itself: after the challenge
			// expired (10 min) solve a new one; after an error (challenge
			// fetch failed, rate limited) try again in a few seconds.
			if (state === 'expired') void refresh();
			if (state === 'error') {
				clearTimeout(errorRetryTimer);
				errorRetryTimer = setTimeout(() => {
					if (!payload && !solving) void refresh();
				}, 5000);
			}
		};
		// Browser-only: the package registers a custom element.
		import('altcha').then(async () => {
			if (cancelled || !el) return;
			el.addEventListener('statechange', onState);
			await el.configure({ challenge: api.signupChallengeURL(), credentials: 'omit', auto: 'onload' });
			ready = true;
		});
		return () => {
			cancelled = true;
			clearTimeout(errorRetryTimer);
			el?.removeEventListener('statechange', onState);
		};
	});

	/** Solve a fresh challenge (expired, or the gateway refused the last
	 *  one). Resolves to the new payload, or null if it couldn't be solved.
	 *  Calls while one is running share it. */
	export function refresh(): Promise<string | null> {
		if (!el || !ready) return Promise.resolve(null);
		if (solving) return solving;
		const w = el;
		payload = null;
		solving = (async () => {
			try {
				w.reset();
				const r = await withTimeout(w.verify().then((v) => v?.payload ?? null));
				payload = r;
				return payload;
			} catch {
				return null;
			} finally {
				solving = null;
			}
		})();
		return solving;
	}

	/** The current payload, or wait for one: the running solve, else a new
	 *  one. Used at submit time so a slow or stalled widget doesn't block. */
	export async function ensure(): Promise<string | null> {
		if (payload) return payload;
		if (solving) return solving;
		if (widgetRun) {
			const p = await withTimeout(widgetRun.promise);
			if (p) return p;
		}
		return refresh();
	}
</script>

<altcha-widget bind:this={el}></altcha-widget>
