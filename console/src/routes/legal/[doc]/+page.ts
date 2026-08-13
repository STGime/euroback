import { error } from '@sveltejs/kit';
import { isLegalDoc, renderLegal } from '$lib/legal.js';
import type { PageLoad } from './$types';

// Load fn runs on both server and client for SPA-style nav; the
// `?raw` md imports resolve at build time so this is a pure string
// transform — no fs, no network.
export const load: PageLoad = ({ params }) => {
	if (!isLegalDoc(params.doc)) {
		throw error(404, `no such legal document: ${params.doc}`);
	}
	return renderLegal(params.doc);
};
