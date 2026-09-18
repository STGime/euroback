/**
 * Svelte stores for Eurobase console state.
 */

import { writable, derived } from 'svelte/store';
import { api, type Project } from './api.js';

// ---- User / auth ----

interface User {
	token: string;
	email: string;
}

function createUserStore() {
	let initial: User | null = null;

	if (typeof localStorage !== 'undefined') {
		const token = localStorage.getItem('eurobase_token');
		const email = localStorage.getItem('eurobase_email');
		if (token && email) {
			initial = { token, email };
		}
	}

	const store = writable<User | null>(initial);

	// Rehydrate on cross-tab localStorage changes. Without this, a tab
	// that was open before another tab signed in (or before SSO landed
	// in a redirect tab) keeps rendering a stale in-memory copy — the
	// pill can end up displaying an email that doesn't match the JWT
	// the API layer actually sends. api.ts already reads the token
	// fresh via localStorage.getItem on every fetch, so requests are
	// correct; only the UI display goes stale.
	if (typeof window !== 'undefined') {
		window.addEventListener('storage', (e) => {
			if (e.key !== 'eurobase_token' && e.key !== 'eurobase_email') {
				return;
			}
			const token = localStorage.getItem('eurobase_token');
			const email = localStorage.getItem('eurobase_email');
			store.set(token && email ? { token, email } : null);
		});
	}

	return {
		subscribe: store.subscribe,
		set: (value: User | null) => {
			if (value) {
				if (typeof localStorage !== 'undefined') {
					localStorage.setItem('eurobase_token', value.token);
					localStorage.setItem('eurobase_email', value.email);
				}
			}
			store.set(value);
		},
		update: store.update
	};
}

export const user = createUserStore();
export const isAuthenticated = derived(user, ($user) => $user !== null);

// ---- Projects ----

export const projects = writable<Project[]>([]);
export const projectsLoading = writable<boolean>(false);
export const projectsError = writable<string | null>(null);

/**
 * Fetch projects from the API and update the store.
 * Safe to call multiple times — sets loading/error state.
 */
export async function loadProjects(): Promise<void> {
	projectsLoading.set(true);
	projectsError.set(null);
	try {
		const list = await api.listProjects();
		projects.set(list);
	} catch (err) {
		const message = err instanceof Error ? err.message : 'Failed to load projects';
		projectsError.set(message);
	} finally {
		projectsLoading.set(false);
	}
}

/**
 * Clear all auth state and redirect to login.
 */
export function logout(): void {
	api.clearToken();
	if (typeof localStorage !== 'undefined') {
		localStorage.removeItem('eurobase_token');
		localStorage.removeItem('eurobase_email');
	}
	user.set(null);
}
