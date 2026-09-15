import { redirect } from '@sveltejs/kit';

// The console root URL (`console.eurobase.app`) should land a fresh
// visitor directly on the signup screen — that's the growth-critical
// path. Logged-in users typing the bare URL will pass through
// /login?signup=1 and the login page's own SSO-callback / redirect
// hooks will still send them onwards on the next interaction; a
// pure "already-authenticated → /projects" bounce needs client-side
// state (session is in localStorage, not a cookie) and can be added
// later without changing this redirect.
export const load = () => {
	throw redirect(307, '/login?signup=1');
};

// Trim the client bundle — no dynamic data to hydrate on this route.
export const prerender = false;
export const ssr = true;
