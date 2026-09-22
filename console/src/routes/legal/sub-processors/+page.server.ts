import { PUBLIC_API_URL } from '$env/static/public';
import type { PageServerLoad } from './$types';

// The DPA (Section 7 + Annex 3), the Privacy Policy and the Terms all
// cite "/legal/sub-processors" as the canonical, always-current list of
// authorised sub-processors. This page is that list, rendered from the
// platform's sub_processors registry via the anonymous
// /platform/public/sub-processors endpoint — the same table that feeds
// every project's Article 30 report, so the legal reference and the
// per-project view can never disagree.
//
// Server-only load: the page is public and must render for crawlers,
// auditors and customers' DPOs without JavaScript or an account. Not
// prerendered — the list is live data and changes with a 30-day notice
// period, so it must reflect the registry at request time.
export const prerender = false;

export interface SubProcessor {
	id: string;
	name: string;
	legal_entity: string;
	country: string;
	country_code: string;
	jurisdiction: string;
	service: string;
	purpose: string;
	data_categories: string[];
	data_subjects: string;
	transfer_mechanism: string;
	security_certs: string[];
	dpa_url?: string;
	privacy_url?: string;
	cloud_act_risk: boolean;
	added_at: string;
}

export const load: PageServerLoad = async ({ fetch }) => {
	try {
		const resp = await fetch(`${PUBLIC_API_URL}/platform/public/sub-processors`, {
			headers: { Accept: 'application/json' }
		});
		if (!resp.ok) {
			return { processors: [] as SubProcessor[], unavailable: true };
		}
		const processors = (await resp.json()) as SubProcessor[];
		return { processors, unavailable: false };
	} catch {
		return { processors: [] as SubProcessor[], unavailable: true };
	}
};
