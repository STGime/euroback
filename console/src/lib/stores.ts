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
