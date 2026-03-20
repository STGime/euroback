/**
 * Eurobase API client.
 *
 * Talks to the Go gateway. The token is stored in localStorage and attached
 * as a Bearer token on every request.
 *
 * The login() method is a placeholder — it will be replaced by Hanko
 * authentication once integrated.
 */

import { PUBLIC_API_URL } from '$env/static/public';

export interface Project {
	id: string;
	name: string;
	slug: string;
	region: string;
	plan: string;
	status: string;
	api_url: string;
	created_at: string;
}

const TOKEN_KEY = 'eurobase_token';

export class EurobaseAPI {
	private baseURL: string;

	constructor(baseURL?: string) {
		this.baseURL = baseURL ?? PUBLIC_API_URL ?? '/api';
	}

	// ---- token helpers ----

	getToken(): string | null {
		if (typeof localStorage === 'undefined') return null;
		return localStorage.getItem(TOKEN_KEY);
	}

	setToken(token: string): void {
		localStorage.setItem(TOKEN_KEY, token);
	}

	clearToken(): void {
		localStorage.removeItem(TOKEN_KEY);
	}

	// ---- internal fetch wrapper ----

	private async fetch<T>(path: string, options: RequestInit = {}): Promise<T> {
		const token = this.getToken();
		const headers: Record<string, string> = {
			'Content-Type': 'application/json',
			...(options.headers as Record<string, string> | undefined)
		};
		if (token) {
			headers['Authorization'] = `Bearer ${token}`;
		}

		const res = await fetch(`${this.baseURL}${path}`, {
			...options,
			headers
		});

		if (!res.ok) {
			const body = await res.text().catch(() => '');
			throw new Error(`API ${res.status}: ${body || res.statusText}`);
		}

		// Handle 204 No Content
		if (res.status === 204) return undefined as unknown as T;

		return res.json() as Promise<T>;
	}

	// ---- public methods ----

	/**
	 * Placeholder login — will be replaced by Hanko passkey / email OTP flow.
	 * For now it just stores a fake token so the console can be navigated.
	 */
	async login(email: string, _password: string): Promise<{ token: string }> {
		// TODO: Replace with real Hanko authentication.
		// In the real flow Hanko issues a JWT that the Go gateway validates.
		const fakeToken = `dev_${btoa(email)}_${Date.now()}`;
		this.setToken(fakeToken);
		return { token: fakeToken };
	}

	/** List all projects (tenants) for the authenticated user. */
	async listProjects(): Promise<Project[]> {
		return this.fetch<Project[]>('/v1/tenants');
	}

	/** Create a new project (tenant). */
	async createProject(data: {
		name: string;
		slug?: string;
		region?: string;
		plan?: string;
	}): Promise<Project> {
		return this.fetch<Project>('/v1/tenants', {
			method: 'POST',
			body: JSON.stringify(data)
		});
	}
}

export const api = new EurobaseAPI();
