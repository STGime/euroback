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

	onMount(() => {
		let cancelled = false;
		const onState = (ev: Event) => {
			const d = (ev as CustomEvent<{ state: string; payload?: string }>).detail;
			payload = d?.state === 'verified' && d.payload ? d.payload : null;
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
			el?.removeEventListener('statechange', onState);
		};
	});

	/** Solve a fresh challenge (after the gateway refused the last one).
	 *  Resolves to the new payload, or null if it couldn't be solved. */
	export async function refresh(): Promise<string | null> {
		if (!el || !ready) return null;
		payload = null;
		el.reset();
		const r = await el.verify();
		payload = r?.payload ?? null;
		return payload;
	}
</script>

<altcha-widget bind:this={el}></altcha-widget>
