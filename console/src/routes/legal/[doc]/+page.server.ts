import { error } from '@sveltejs/kit';
import { isLegalDoc, renderLegal } from '$lib/legal.js';
import type { PageServerLoad } from './$types';

// Server-only load. Deliberately NOT +page.ts, because a universal
// (client-runnable) load would bundle the raw .md sources — including
// the leading HTML-comment reviewer notes ("do NOT publish with
// placeholders", "Lawyer review required", etc.) and every {{TOKEN}}
// placeholder — into the public client JS. Users would get a clean
// rendered page but anyone reading the served JS would see the
// internal legal-review state attached to documents they're accepting.
//
// Keeping the load server-only means the client receives only the
// substituted, sanitized HTML via SvelteKit's data endpoint; SPA-
// style nav still works. As a bonus, marked (~36 KB) and the three
// .md sources drop out of the client bundle entirely.
//
// If you ever import LEGAL_VERSION from $lib/legal into a client-
// runnable file, Vite will pull the whole module (and the ?raw
// imports) back into the client bundle. Duplicate the const there
// (see login/+page.svelte:26 which does exactly this deliberately)
// or extract just the version constant to its own tiny module.
export const load: PageServerLoad = ({ params }) => {
	if (!isLegalDoc(params.doc)) {
		throw error(404, `no such legal document: ${params.doc}`);
	}
	return renderLegal(params.doc);
};
