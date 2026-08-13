// Console-hosted legal documents: DPA / Terms / Privacy Policy.
//
// The signup form asks users to accept the Terms + DPA and to
// acknowledge the Privacy Policy. Before this file existed the
// links pointed at https://www.eurobase.app/{dpa,terms,privacy}
// which the marketing-site SPA serves as its 404-shell — users
// were click-through-consenting to an empty page. See the fix in
// PR fix-legal-links-console-hosted.
//
// We render the .md sources at build time via Vite `?raw` imports,
// so no `fs` access at runtime and no path-traversal surface: the
// set of accessible documents is exactly what's imported below.
//
// Placeholder substitution and reviewer-notes stripping live here
// (not on the +page.server route) so any future consumer (e.g. an
// admin preview endpoint) gets the same rendered output.

import { marked } from 'marked';

// Vite `?raw` bundles the .md file as a string at build time. The
// path is relative to THIS file; adjust if the file moves.
import dpaSource from '../../../docs/legal/v2/dpa.md?raw';
import termsSource from '../../../docs/legal/v2/terms.md?raw';
import privacySource from '../../../docs/legal/v2/privacy.md?raw';

// LEGAL_VERSION mirrors the click-through consent version tracked
// against legal_acceptances (login/+page.svelte line 26 pins the
// same '2.0'). Bump BOTH when publishing a new revision so users
// get a fresh acceptance prompt.
export const LEGAL_VERSION = '2.0';

// Placeholder values sourced from the repo (docs/emails/2026-07-19-
// beta-update.html footer + CLAUDE.md's Estonian VAT section +
// dpo@eurobase.app usage across docs/legal/v2/*). If a future
// change moves any of these into a config file, prefer that source
// over this const — the goal is one source of truth.
//
// EFFECTIVE_DATE is set to the beta-update date that announced
// "v2 Terms + DPA are already live". That's when click-through
// acceptance actually went into effect.
const PLACEHOLDERS: Record<string, string> = {
	LEGAL_ENTITY: 'Eurobase OÜ',
	REGISTERED_ADDRESS: 'Ahtri 12, Tallinn 15551, Estonia',
	REGISTRY_NUMBER: '17557586',
	VAT_NUMBER: 'Not VAT-registered under Estonian VAT Act §19',
	EFFECTIVE_DATE: '2026-07-19',
	// Every email placeholder currently resolves to the DPO
	// mailbox — that's the only contact address confirmed and
	// consistently used across docs/legal/v2/*. Split into
	// dedicated inboxes (support@ / legal@ / withdraw@) whenever
	// ops sets those up.
	SUPPORT_EMAIL: 'dpo@eurobase.app',
	NOTICES_EMAIL: 'dpo@eurobase.app',
	WITHDRAWAL_EMAIL: 'dpo@eurobase.app',
	CONTACT_EMAIL: 'dpo@eurobase.app',
};

type LegalDoc = 'dpa' | 'terms' | 'privacy';

const DOC_META: Record<LegalDoc, { source: string; title: string }> = {
	dpa: { source: dpaSource, title: 'Data Processing Agreement' },
	terms: { source: termsSource, title: 'Terms of Service' },
	privacy: { source: privacySource, title: 'Privacy Policy' },
};

export function isLegalDoc(slug: string): slug is LegalDoc {
	return slug === 'dpa' || slug === 'terms' || slug === 'privacy';
}

// stripReviewerNotes removes the leading HTML comment block that
// every docs/legal/v2/*.md file uses to carry publication-time
// reviewer notes. Those aren't for end-users and shouldn't render.
// The comment always starts at BOF and ends at the first `-->`.
function stripReviewerNotes(md: string): string {
	if (!md.startsWith('<!--')) return md;
	const end = md.indexOf('-->');
	if (end < 0) return md;
	return md.slice(end + 3).trimStart();
}

// stripLeadingH1 drops the first `# Title` from the md body so the
// page-level <h1> in +page.svelte isn't shadowed by an identical one
// right below it (cosmetic + a11y). Matches only the FIRST heading
// so any deeper structure is preserved.
function stripLeadingH1(md: string): string {
	const trimmed = md.trimStart();
	if (!trimmed.startsWith('# ')) return md;
	const nl = trimmed.indexOf('\n');
	if (nl < 0) return '';
	return trimmed.slice(nl + 1).trimStart();
}

function substitute(md: string): string {
	return md.replace(/\{\{([A-Z_]+)\}\}/g, (match, key) => {
		return PLACEHOLDERS[key] ?? match;
	});
}

// renderLegal returns the rendered HTML + title for a doc slug.
// Throws when the slug isn't recognized so the +page.server.ts load
// fn can turn that into a 404 (rather than silently blanking).
//
// SECURITY: marked does NOT sanitize HTML by default in v18 — raw
// tags, event-handler attributes, and even `javascript:` hrefs pass
// straight through to the {@html} sink. That's acceptable here
// because the ONLY input is build-time repo markdown (the three
// `?raw` imports at the top of this file), and those files contain
// no inline HTML after stripReviewerNotes drops the leading comment
// block. If a future change ever points this function at user-
// supplied markdown, swap in DOMPurify (or marked's `renderer`
// override with tag escaping) BEFORE that change lands — do not
// trust this function with untrusted input as-is.
export function renderLegal(slug: string): { title: string; html: string; version: string } {
	if (!isLegalDoc(slug)) {
		throw new Error(`unknown legal document: ${slug}`);
	}
	const { source, title } = DOC_META[slug];
	const md = substitute(stripLeadingH1(stripReviewerNotes(source)));
	const html = marked.parse(md, { async: false }) as string;
	return { title, html, version: LEGAL_VERSION };
}
