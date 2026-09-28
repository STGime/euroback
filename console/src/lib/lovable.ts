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

const LOVABLE_EDITOR_HOSTS = new Set(['lovable.dev', 'www.lovable.dev']);
const LOVABLE_PROJECT_ID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/**
 * The app origin for a URL pasted into the Lovable tab. The address bar
 * while working in Lovable shows the *editor* (lovable.dev/projects/<id>),
 * but the preview app runs in an iframe on its own origin,
 * https://id-preview--<id>.lovable.app — so an editor URL is turned into
 * that (only for the preview field: `preview`), and lovable.dev itself
 * (never an app origin) is refused with a hint.
 */
export function lovableAppOrigin(raw: string, preview = false): { origin: string | null; note?: string; error?: string } {
	const s = raw.trim();
	let u: URL;
	try {
		u = new URL(s);
	} catch {
		return { origin: null, error: 'Not a URL — paste the full address, starting with https://' };
	}
	if (LOVABLE_EDITOR_HOSTS.has(u.hostname.replace(/\.$/, ''))) {
		const id = u.pathname.match(/^\/projects\/([^/?#]+)/)?.[1] ?? '';
		if (preview && LOVABLE_PROJECT_ID.test(id)) {
			const origin = `https://id-preview--${id.toLowerCase()}.lovable.app`;
			return { origin, note: `That's the Lovable editor — using your app's preview address, ${origin}` };
		}
		return {
			origin: null,
			error: preview
				? "That's the Lovable editor (lovable.dev), not your app. Paste your project's editor URL (lovable.dev/projects/…) or the preview address (https://id-preview--….lovable.app)."
				: "That's the Lovable editor (lovable.dev), not your app. Paste the address your app is published at (Lovable → Publish)."
		};
	}
	const origin = appOrigin(s);
	return origin ? { origin } : { origin: null, error: 'Use an https:// address (http only for localhost).' };
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
