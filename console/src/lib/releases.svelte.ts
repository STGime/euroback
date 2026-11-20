// Latest released versions of the SDK and the CLI, read once per page load
// from the platform (GET /v1/meta/releases — the gateway reads npm and the
// CLI's GitHub releases and caches them, so browsers never contact those).
// Until the answer arrives (and on prerendered pages, before hydration) the
// values below are shown; keep them roughly current when releasing, they're
// only the fallback (the SDK and CLI release checklists in CLAUDE.md say so).
import { PUBLIC_API_URL } from '$env/static/public';

export const FALLBACK_SDK_VERSION = '0.13.1';
export const FALLBACK_CLI_VERSION = '0.7.2';

export interface LatestReleases {
	sdk: string;
	cli: string;
	sdkUrl: string;
	cliUrl: string;
	/** True once the live versions have been read. */
	live: boolean;
}

const releases = $state<LatestReleases>({
	sdk: FALLBACK_SDK_VERSION,
	cli: FALLBACK_CLI_VERSION,
	sdkUrl: 'https://www.npmjs.com/package/@eurobase/sdk',
	cliUrl: 'https://github.com/STGime/homebrew-tap/releases/latest',
	live: false
});

let requested = false;

const VERSION = /^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/;

/** The latest versions; starts the lookup on first use in the browser. */
export function latestReleases(): LatestReleases {
	if (!requested && typeof window !== 'undefined') {
		requested = true;
		const base = (PUBLIC_API_URL || '/api').replace(/\/+$/, '');
		fetch(`${base}/v1/meta/releases`)
			.then((res) => (res.ok ? res.json() : null))
			.then((body) => {
				if (!body) return;
				// Only the version numbers are taken from the response; the
				// links are built here from them.
				if (body.sdk && typeof body.sdk.version === 'string' && VERSION.test(body.sdk.version)) {
					releases.sdk = body.sdk.version;
					releases.sdkUrl = `https://www.npmjs.com/package/@eurobase/sdk/v/${body.sdk.version}`;
				}
				if (body.cli && typeof body.cli.version === 'string' && VERSION.test(body.cli.version)) {
					releases.cli = body.cli.version;
					releases.cliUrl = `https://github.com/STGime/homebrew-tap/releases/tag/cli-v${body.cli.version}`;
				}
				releases.live = true;
			})
			.catch(() => {
				// Offline or blocked: the fallback versions stay.
			});
	}
	return releases;
}
