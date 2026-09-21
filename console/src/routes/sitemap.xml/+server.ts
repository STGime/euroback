import { pageChapters } from '$lib/docs/registry';

// Pre-rendered at build time alongside the docs. Lists only the public,
// server-rendered surfaces of the console; app routes are client-only
// and disallowed in robots.txt.
export const prerender = true;

const ORIGIN = 'https://console.eurobase.app';

export function GET(): Response {
	// "/" is a 307 to /login and is deliberately absent. Legal slugs mirror
	// isLegalDoc() in $lib/legal.ts (server-only module, not importable here).
	const paths = [
		'/pricing',
		'/legal',
		'/legal/terms',
		'/legal/privacy',
		'/legal/dpa',
		'/docs',
		...pageChapters.map((c) => `/docs/${c.id}`)
	];
	const body =
		'<?xml version="1.0" encoding="UTF-8"?>\n' +
		'<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n' +
		paths.map((p) => `  <url><loc>${ORIGIN}${p}</loc></url>`).join('\n') +
		'\n</urlset>\n';
	return new Response(body, {
		headers: { 'Content-Type': 'application/xml; charset=utf-8', 'Cache-Control': 'public, max-age=86400' }
	});
}
