// Session details the console needs for UX decisions (never for access
// control — the gateway enforces). Read from the platform JWT's payload.

/** The org this session is SSO-signed-in to, or '' (password / passkey / unknown). */
export function sessionSSOOrgId(token: string | null | undefined): string {
	if (!token) return '';
	try {
		const part = token.split('.')[1] ?? '';
		const json = atob(part.replace(/-/g, '+').replace(/_/g, '/').padEnd(Math.ceil(part.length / 4) * 4, '='));
		const claims = JSON.parse(json) as { login_via?: string; sso_org_id?: string };
		return claims.login_via === 'sso' ? (claims.sso_org_id ?? '') : '';
	} catch {
		return '';
	}
}

/**
 * Default owner for a new project (#710): the first admin org whose
 * projects this session can open — an SSO-required org only when the
 * session is SSO-signed-in to it — else Personal. Otherwise the default
 * would create a project the user can't open.
 */
export function defaultProjectOwner(
	adminOrgs: { id: string; sso_required?: boolean }[],
	token: string | null | undefined
): string {
	const ssoOrg = sessionSSOOrgId(token);
	const ok = adminOrgs.find((o) => !o.sso_required || o.id === ssoOrg);
	return ok ? ok.id : 'personal';
}
