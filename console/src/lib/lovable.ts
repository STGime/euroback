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

/** Both origins a Lovable project's preview runs on (see lovableAppOrigins). */
export function lovablePreviewOrigins(projectId: string): string[] {
	const id = projectId.toLowerCase();
	return [`https://${id}.lovableproject.com`, `https://id-preview--${id}.lovable.app`];
}

/** The Lovable project id in a preview-type URL (editor, either preview host), or ''. */
function lovablePreviewProjectId(u: URL): string {
	const host = u.hostname.replace(/\.$/, '');
	let id = '';
	if (LOVABLE_EDITOR_HOSTS.has(host)) id = u.pathname.match(/^\/projects\/([^/?#]+)/)?.[1] ?? '';
	else if (host.endsWith('.lovableproject.com')) id = host.slice(0, -'.lovableproject.com'.length);
	else if (host.startsWith('id-preview--') && host.endsWith('.lovable.app')) id = host.slice('id-preview--'.length, -'.lovable.app'.length);
	return LOVABLE_PROJECT_ID.test(id) ? id : '';
}

// A published Lovable subdomain (<name>.lovable.app) we derive a preview
// origin for: one DNS label short enough that "preview--" + name is still
// one (≤ 63), and with no "--". Lovable's router uses "<prefix>--<name>"
// (preview--, id-preview--) to tell previews from apps, so we rely on
// Lovable reserving "--" labels — a name containing "--" (incl. xn--
// punycode) is ambiguous under that grammar, so it gets no derived origin
// (its own origin only, which is always safe).
const LOVABLE_APP_NAME = /^[a-z0-9](?:[a-z0-9-]{0,52}[a-z0-9])?$/;

/**
 * The published app's name for a `<name>.lovable.app` or
 * `preview--<name>.lovable.app` URL, or '' (other hosts, the id-preview
 * host, nested subdomains).
 */
function lovablePublishedName(u: URL): string {
	const host = u.hostname.replace(/\.$/, '');
	if (!host.endsWith('.lovable.app')) return '';
	let name = host.slice(0, -'.lovable.app'.length);
	if (name.startsWith('id-preview--')) return '';
	if (name.startsWith('preview--')) name = name.slice('preview--'.length);
	return LOVABLE_APP_NAME.test(name) && !name.includes('--') ? name : '';
}

/**
 * The app origins for a URL pasted into the Lovable tab.
 *
 * A Lovable project's preview runs on two origins (verified 2026-09-28 in
 * the gateway's CORS log): inside the editor, the preview pane is an iframe
 * on https://<id>.lovableproject.com; opened in its own tab it is
 * https://id-preview--<id>.lovable.app. The address bar while working in
 * Lovable shows neither — it shows the editor, lovable.dev/projects/<id>.
 * So in the preview field (`preview`) any of the three gives both preview
 * origins; lovable.dev itself (never an app origin) is refused with a hint.
 * A published app (https://<name>.lovable.app) also has a shareable
 * preview on https://preview--<name>.lovable.app (the same live test):
 * either one, in any field, gives both. Other URLs give their own origin
 * (https only; http for localhost).
 */
export function lovableAppOrigins(raw: string, preview = false): { origins: string[]; note?: string; error?: string } {
	const s = raw.trim();
	let u: URL;
	try {
		u = new URL(s);
	} catch {
		return { origins: [], error: 'Not a URL — paste the full address, starting with https://' };
	}
	const id = preview ? lovablePreviewProjectId(u) : '';
	if (id) {
		const origins = lovablePreviewOrigins(id);
		return { origins, note: `Lovable preview — adding both preview addresses: ${origins.join(' and ')}` };
	}
	if (LOVABLE_EDITOR_HOSTS.has(u.hostname.replace(/\.$/, ''))) {
		return {
			origins: [],
			error: preview
				? "That's the Lovable editor (lovable.dev), not your app. Paste your project's editor URL from the address bar (lovable.dev/projects/…)."
				: "That's the Lovable editor (lovable.dev), not your app. Paste the address your app is published at (Lovable → Publish)."
		};
	}
	const name = u.protocol === 'https:' && !u.username && !u.password && !u.port ? lovablePublishedName(u) : '';
	if (name) {
		const origins = [`https://${name}.lovable.app`, `https://preview--${name}.lovable.app`];
		return { origins, note: `Lovable app — adding it and its shareable preview: ${origins.join(' and ')}` };
	}
	const origin = appOrigin(s);
	return origin ? { origins: [origin] } : { origins: [], error: 'Use an https:// address (http only for localhost).' };
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
