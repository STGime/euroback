import type { Handle } from '@sveltejs/kit';

// Only the server-rendered marketing surfaces should appear in search.
// Everything else (/login incl. the ?signup=1 form, the client-only app)
// gets a noindex header instead of a robots.txt Disallow: a Disallow stops
// Google from crawling the page, so it never sees the noindex and indexes
// the bare URL anyway ("Indexed, though blocked by robots.txt").
const INDEXABLE = ['/signup', '/docs', '/pricing', '/legal', '/sitemap.xml', '/robots.txt'];

function isIndexable(pathname: string): boolean {
	return INDEXABLE.some((p) => pathname === p || pathname.startsWith(p + '/'));
}

export const handle: Handle = async ({ event, resolve }) => {
	const response = await resolve(event);
	if (!isIndexable(event.url.pathname) && !event.url.pathname.startsWith('/_app/')) {
		response.headers.set('X-Robots-Tag', 'noindex, nofollow');
	}
	return response;
};
