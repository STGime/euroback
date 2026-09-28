// The Eurobase skill for Lovable (Connect → Lovable tab). Source of truth:
// integrations/lovable/ (also the content of the public skill repo).
import skillSource from '../../../integrations/lovable/SKILL.md?raw';
import sdkReferenceSource from '../../../integrations/lovable/references/sdk-reference.md?raw';

export const lovableSkill: string = skillSource;
export const lovableSdkReference: string = sdkReferenceSource;

/** Origin (scheme://host[:port]) of an app URL, or null if it isn't a usable http(s) URL. */
export function appOrigin(raw: string): string | null {
	const s = raw.trim();
	if (!s) return null;
	try {
		const u = new URL(s);
		if (u.protocol !== 'https:' && !(u.protocol === 'http:' && (u.hostname === 'localhost' || u.hostname === '127.0.0.1'))) {
			return null;
		}
		if (u.username || u.password) return null;
		return u.origin;
	} catch {
		return null;
	}
}

/** The pages the skill builds for email and OAuth flows. */
export const LOVABLE_AUTH_PATHS = ['/verify', '/reset-password', '/magic', '/auth/callback'];

/**
 * What to add for one app origin: the origin as a CORS origin; the origin
 * itself (OAuth accepts any path under an entry without a path) and the
 * skill's email-flow pages as redirect URLs (email flows need exact URLs).
 */
export function lovableEntries(origin: string): { cors: string[]; redirects: string[] } {
	return { cors: [origin], redirects: [origin, ...LOVABLE_AUTH_PATHS.map((p) => origin + p)] };
}
