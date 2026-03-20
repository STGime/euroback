import { browser } from '$app/environment';
import { redirect } from '@sveltejs/kit';
import type { PageLoad } from './$types';

export const load: PageLoad = () => {
	if (browser) {
		const token = localStorage.getItem('eurobase_token');
		if (!token) {
			throw redirect(302, '/login');
		}
	}
};
