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
