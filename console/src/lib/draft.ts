// Unsent form drafts, kept per browser tab (sessionStorage) so a form
// survives the sign-in redirect when the session expires. Best effort:
// storage can be unavailable (private mode, blocked site data).

export function saveDraft(key: string, value: unknown): void {
	try {
		sessionStorage.setItem(key, JSON.stringify(value));
	} catch { /* storage unavailable */ }
}

export function loadDraft<T>(key: string): T | null {
	try {
		const raw = sessionStorage.getItem(key);
		return raw ? (JSON.parse(raw) as T) : null;
	} catch {
		return null;
	}
}

export function clearDraft(key: string): void {
	try {
		sessionStorage.removeItem(key);
	} catch { /* storage unavailable */ }
}

/** Key prefix for every form draft, so sign-out can remove them all. */
export const DRAFT_PREFIX = 'eurobase_draft:';

/** Removes every form draft in this tab (sign-out: the next person on
 * this tab must not get the previous user's unsent forms). */
export function clearAllDrafts(): void {
	try {
		for (let i = sessionStorage.length - 1; i >= 0; i--) {
			const k = sessionStorage.key(i);
			if (k?.startsWith(DRAFT_PREFIX)) sessionStorage.removeItem(k);
		}
	} catch { /* storage unavailable */ }
}
