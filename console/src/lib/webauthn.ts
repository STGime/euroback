/**
 * Browser-side WebAuthn helpers for console passkeys (#621).
 *
 * The gateway (go-webauthn) sends options as JSON with every binary
 * field base64url-encoded, and expects the credential back in the same
 * shape. The native `PublicKeyCredential.parseCreationOptionsFromJSON`
 * / `toJSON()` do exactly this but aren't available in every browser we
 * support yet, so the conversion is done by hand here.
 */

function b64urlToBuffer(value: string): ArrayBuffer {
	const pad = '='.repeat((4 - (value.length % 4)) % 4);
	const b64 = (value + pad).replace(/-/g, '+').replace(/_/g, '/');
	const bin = atob(b64);
	const bytes = new Uint8Array(bin.length);
	for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
	return bytes.buffer;
}

function bufferToB64url(buf: ArrayBuffer | null | undefined): string | undefined {
	if (!buf) return undefined;
	const bytes = new Uint8Array(buf);
	let bin = '';
	for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
	return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

type JSONDescriptor = { id: string; type: string; transports?: string[] };

function toDescriptors(list?: JSONDescriptor[]): PublicKeyCredentialDescriptor[] | undefined {
	return list?.map((d) => ({
		id: b64urlToBuffer(d.id),
		type: d.type as PublicKeyCredentialType,
		transports: d.transports as AuthenticatorTransport[] | undefined
	}));
}

/** True when this browser can use passkeys at all. */
export function passkeysSupported(): boolean {
	return typeof window !== 'undefined' && typeof window.PublicKeyCredential !== 'undefined' && !!navigator.credentials;
}

/**
 * Human message for a WebAuthn DOMException. NotAllowedError is what
 * browsers throw for "user cancelled" AND "timed out", so it's worded
 * to cover both.
 */
export function passkeyErrorMessage(err: unknown): string {
	if (err instanceof DOMException) {
		switch (err.name) {
			case 'NotAllowedError':
				return 'Passkey request was cancelled or timed out.';
			case 'InvalidStateError':
				return 'This passkey is already registered on your account.';
			case 'SecurityError':
				return 'Passkeys are not available on this domain.';
			case 'NotSupportedError':
				return 'This device does not support passkeys.';
		}
	}
	return err instanceof Error ? err.message : 'Passkey request failed.';
}

/** Run navigator.credentials.create for a registration `options` object from the gateway. */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export async function createPasskey(options: any): Promise<Record<string, unknown>> {
	const pk = options.publicKey;
	const publicKey: PublicKeyCredentialCreationOptions = {
		...pk,
		challenge: b64urlToBuffer(pk.challenge),
		user: { ...pk.user, id: b64urlToBuffer(pk.user.id) },
		excludeCredentials: toDescriptors(pk.excludeCredentials)
	};
	const cred = (await navigator.credentials.create({ publicKey })) as PublicKeyCredential | null;
	if (!cred) throw new Error('No passkey was created.');
	const res = cred.response as AuthenticatorAttestationResponse;
	return {
		id: cred.id,
		rawId: bufferToB64url(cred.rawId),
		type: cred.type,
		authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
		clientExtensionResults: cred.getClientExtensionResults(),
		response: {
			clientDataJSON: bufferToB64url(res.clientDataJSON),
			attestationObject: bufferToB64url(res.attestationObject),
			transports: typeof res.getTransports === 'function' ? res.getTransports() : undefined
		}
	};
}

/** Run navigator.credentials.get for an assertion `options` object from the gateway. */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export async function getPasskeyAssertion(options: any): Promise<Record<string, unknown>> {
	const pk = options.publicKey;
	const publicKey: PublicKeyCredentialRequestOptions = {
		...pk,
		challenge: b64urlToBuffer(pk.challenge),
		allowCredentials: toDescriptors(pk.allowCredentials)
	};
	const cred = (await navigator.credentials.get({ publicKey })) as PublicKeyCredential | null;
	if (!cred) throw new Error('No passkey was selected.');
	const res = cred.response as AuthenticatorAssertionResponse;
	return {
		id: cred.id,
		rawId: bufferToB64url(cred.rawId),
		type: cred.type,
		authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
		clientExtensionResults: cred.getClientExtensionResults(),
		response: {
			clientDataJSON: bufferToB64url(res.clientDataJSON),
			authenticatorData: bufferToB64url(res.authenticatorData),
			signature: bufferToB64url(res.signature),
			userHandle: bufferToB64url(res.userHandle)
		}
	};
}
