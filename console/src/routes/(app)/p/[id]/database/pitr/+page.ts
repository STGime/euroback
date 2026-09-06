import { redirect } from '@sveltejs/kit';
import type { PageLoad } from './$types';

// PITR-to-timestamp was dropped from Scaleway RDB's CLI + REST API
// in September 2026 (see euroback#520). The customer-visible restore
// surface is snapshot-based only now — take an on-demand snapshot
// via the Backups tab, restore from it if needed. This route stays
// as a permanent redirect so any bookmarked or shared /pitr URL
// lands the operator on the right tab instead of 404-ing.
export const load: PageLoad = ({ params }) => {
	throw redirect(302, `/p/${params.id}/database/backups`);
};
