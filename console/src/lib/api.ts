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
	public_key?: string;
	secret_key?: string;
}

export interface ColumnInfo {
	name: string;
	data_type: string;
	is_nullable: boolean;
	default_value?: string | null;
}

export interface TableSchema {
	name: string;
	columns: ColumnInfo[];
	row_count: number;
}

export interface FileInfo {
	key: string;
	content_type: string;
	size: number;
	last_modified: string;
}

export interface FileListResponse {
	objects: FileInfo[];
	next_cursor?: string;
	has_more: boolean;
}

export interface SignedUrlResponse {
	url: string;
	expires_at: string;
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

	/**
	 * Raw fetch that returns the Response object directly.
	 * Used for non-JSON endpoints (file upload, download).
	 */
	private async rawFetch(path: string, options: RequestInit = {}): Promise<Response> {
		const token = this.getToken();
		const headers: Record<string, string> = {
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

		return res;
	}

	// ---- public methods ----

	/** Sign up a new platform user. */
	async signUp(email: string, password: string): Promise<{ access_token: string; user: { id: string; email: string } }> {
		const resp = await this.fetch<{ access_token: string; user: { id: string; email: string } }>('/platform/auth/signup', {
			method: 'POST',
			body: JSON.stringify({ email, password })
		});
		this.setToken(resp.access_token);
		return resp;
	}

	/** Sign in an existing platform user. */
	async signIn(email: string, password: string): Promise<{ access_token: string; user: { id: string; email: string } }> {
		const resp = await this.fetch<{ access_token: string; user: { id: string; email: string } }>('/platform/auth/signin', {
			method: 'POST',
			body: JSON.stringify({ email, password })
		});
		this.setToken(resp.access_token);
		return resp;
	}

	/** List all projects (tenants) for the authenticated user. */
	async listProjects(): Promise<Project[]> {
		return this.fetch<Project[]>('/v1/tenants');
	}

	/** Get a single project by ID. */
	async getProject(projectId: string): Promise<Project> {
		const projects = await this.fetch<Project[]>('/v1/tenants');
		const project = projects.find((p) => p.id === projectId);
		if (!project) throw new Error('Project not found');
		return project;
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
	/** Delete a project (irreversible). */
	async deleteProject(projectId: string): Promise<void> {
		return this.fetch(`/v1/tenants/${projectId}`, { method: 'DELETE' });
	}

	/** Get schema change history for a project. */
	async getSchemaChanges(projectId: string): Promise<SchemaChange[]> {
		return this.fetch<SchemaChange[]>(`/platform/projects/${projectId}/schema/changes`);
	}

	// ---- Database methods ----

	/** Get schema introspection for a project (all tables and columns). */
	async getSchema(projectId: string): Promise<TableSchema[]> {
		return this.fetch<TableSchema[]>(`/platform/projects/${projectId}/schema`);
	}

	/** Create a new table in the project schema. */
	async createTable(
		projectId: string,
		name: string,
		columns: {
			name: string;
			type: string;
			nullable: boolean;
			default_value?: string;
			is_primary_key: boolean;
		}[]
	): Promise<{ status: string; table: string }> {
		return this.fetch(`/platform/projects/${projectId}/schema/tables`, {
			method: 'POST',
			body: JSON.stringify({ name, columns })
		});
	}

	/** Drop a table from the project schema. */
	async dropTable(projectId: string, tableName: string): Promise<void> {
		return this.fetch(`/platform/projects/${projectId}/schema/tables/${tableName}`, {
			method: 'DELETE'
		});
	}

	/** Add a column to an existing table. */
	async addColumn(
		projectId: string,
		tableName: string,
		column: { name: string; type: string; nullable: boolean; default_value?: string }
	): Promise<{ status: string; column: string }> {
		return this.fetch(`/platform/projects/${projectId}/schema/tables/${tableName}/columns`, {
			method: 'POST',
			body: JSON.stringify(column)
		});
	}

	/** Drop a column from a table. */
	async dropColumn(projectId: string, tableName: string, columnName: string): Promise<void> {
		return this.fetch(`/platform/projects/${projectId}/schema/tables/${tableName}/columns/${columnName}`, {
			method: 'DELETE'
		});
	}

	/** Query rows from a table with optional filtering, sorting, and pagination. */
	async queryTable(
		projectId: string,
		table: string,
		params?: {
			select?: string;
			limit?: number;
			offset?: number;
			order?: string;
			filters?: Record<string, string>;
		}
	): Promise<{ data: any[]; count: number }> {
		const searchParams = new URLSearchParams();
		if (params?.select) searchParams.set('select', params.select);
		if (params?.limit != null) searchParams.set('limit', String(params.limit));
		if (params?.offset != null) searchParams.set('offset', String(params.offset));
		if (params?.order) searchParams.set('order', params.order);
		if (params?.filters) {
			for (const [key, value] of Object.entries(params.filters)) {
				searchParams.set(key, value);
			}
		}
		const qs = searchParams.toString();
		const path = `/v1/db/${table}${qs ? `?${qs}` : ''}`;
		return this.fetch<{ data: any[]; count: number }>(path, {
			headers: { 'X-Project-Id': projectId }
		});
	}

	/** Insert a new row into a table. */
	async insertRow(
		projectId: string,
		table: string,
		data: Record<string, any>
	): Promise<any> {
		return this.fetch(`/v1/db/${table}`, {
			method: 'POST',
			body: JSON.stringify(data),
			headers: { 'X-Project-Id': projectId }
		});
	}

	/** Update a row by ID. */
	async updateRow(
		projectId: string,
		table: string,
		id: string,
		data: Record<string, any>
	): Promise<any> {
		return this.fetch(`/v1/db/${table}/${id}`, {
			method: 'PATCH',
			body: JSON.stringify(data),
			headers: { 'X-Project-Id': projectId }
		});
	}

	/** Delete a row by ID. */
	async deleteRow(
		projectId: string,
		table: string,
		id: string
	): Promise<void> {
		return this.fetch(`/v1/db/${table}/${id}`, {
			method: 'DELETE',
			headers: { 'X-Project-Id': projectId }
		});
	}

	// ---- SQL Editor methods ----

	/** Execute a raw SQL SELECT query against the project database. */
	async executeSQL(
		projectId: string,
		sql: string,
		limit?: number
	): Promise<{
		columns: string[];
		rows: Record<string, any>[];
		row_count: number;
		execution_time_ms: number;
	}> {
		return this.fetch(`/v1/db/sql`, {
			method: 'POST',
			body: JSON.stringify({ sql, limit: limit ?? 1000 }),
			headers: { 'X-Project-Id': projectId }
		});
	}

	// ---- Storage methods ----

	/** Upload a file to project storage. */
	async uploadFile(
		slug: string,
		file: File,
		key?: string
	): Promise<{ key: string; content_type: string; size: number }> {
		const formData = new FormData();
		formData.append('file', file);
		if (key) formData.append('key', key);

		const res = await this.rawFetch('/v1/storage/upload', {
			method: 'POST',
			headers: { 'X-Project-Slug': slug },
			body: formData
		});

		return res.json();
	}

	/** Download a file from project storage. */
	async downloadFile(slug: string, key: string): Promise<Blob> {
		// Encode each path segment individually to preserve slashes
		const encoded = key.split('/').map(encodeURIComponent).join('/');
		const res = await this.rawFetch(`/v1/storage/${encoded}`, {
			headers: { 'X-Project-Slug': slug }
		});
		return res.blob();
	}

	/** Delete a file from project storage. */
	async deleteFile(slug: string, key: string): Promise<void> {
		const encoded = key.split('/').map(encodeURIComponent).join('/');
		await this.fetch<void>(`/v1/storage/${encoded}`, {
			method: 'DELETE',
			headers: { 'X-Project-Slug': slug }
		});
	}

	/** List files in project storage. */
	async listFiles(
		slug: string,
		options?: { prefix?: string; limit?: number; cursor?: string }
	): Promise<FileListResponse> {
		const params = new URLSearchParams();
		if (options?.prefix) params.set('prefix', options.prefix);
		if (options?.limit) params.set('limit', String(options.limit));
		if (options?.cursor) params.set('cursor', options.cursor);
		const qs = params.toString();
		return this.fetch<FileListResponse>(`/v1/storage${qs ? `?${qs}` : ''}`, {
			headers: { 'X-Project-Slug': slug }
		});
	}

	/** Generate a signed URL for a file. */
	async generateSignedUrl(
		slug: string,
		key: string,
		operation: 'upload' | 'download',
		expiresIn?: number
	): Promise<SignedUrlResponse> {
		return this.fetch<SignedUrlResponse>('/v1/storage/signed-url', {
			method: 'POST',
			headers: { 'X-Project-Slug': slug },
			body: JSON.stringify({ key, operation, expires_in: expiresIn })
		});
	}

	// ---- Webhook methods ----

	async listWebhooks(projectId: string): Promise<Webhook[]> {
		return this.fetch<Webhook[]>(`/platform/projects/${projectId}/webhooks`);
	}

	async createWebhook(projectId: string, data: { url: string; events: string[]; description?: string }): Promise<Webhook> {
		return this.fetch<Webhook>(`/platform/projects/${projectId}/webhooks`, {
			method: 'POST',
			body: JSON.stringify(data)
		});
	}

	async updateWebhook(projectId: string, webhookId: string, data: { url?: string; events?: string[]; enabled?: boolean; description?: string }): Promise<Webhook> {
		return this.fetch<Webhook>(`/platform/projects/${projectId}/webhooks/${webhookId}`, {
			method: 'PATCH',
			body: JSON.stringify(data)
		});
	}

	async deleteWebhook(projectId: string, webhookId: string): Promise<void> {
		return this.fetch(`/platform/projects/${projectId}/webhooks/${webhookId}`, { method: 'DELETE' });
	}

	async getWebhookDeliveries(projectId: string, webhookId: string): Promise<WebhookDelivery[]> {
		return this.fetch<WebhookDelivery[]>(`/platform/projects/${projectId}/webhooks/${webhookId}/deliveries`);
	}

	// ---- API Key methods ----

	async listAPIKeys(projectId: string): Promise<APIKey[]> {
		return this.fetch<APIKey[]>(`/platform/projects/${projectId}/api-keys`);
	}

	async regenerateAPIKeys(projectId: string): Promise<{ public_key: string; secret_key: string }> {
		return this.fetch(`/platform/projects/${projectId}/api-keys/regenerate`, { method: 'POST' });
	}

	/** Get connection info for IDE integration (CLAUDE.md, .cursorrules, .env). */
	async getConnectInfo(projectId: string): Promise<ConnectInfo> {
		return this.fetch<ConnectInfo>(`/platform/projects/${projectId}/connect`);
	}
}

export interface Webhook {
	id: string;
	project_id: string;
	url: string;
	events: string[];
	secret?: string;
	enabled: boolean;
	description: string;
	created_at: string;
}

export interface WebhookDelivery {
	id: string;
	webhook_id: string;
	event: string;
	payload: any;
	status_code: number | null;
	response: string | null;
	attempts: number;
	success: boolean;
	created_at: string;
}

export interface APIKey {
	id: string;
	key_prefix: string;
	type: string;
	created_at: string;
	last_used_at: string | null;
}

export interface ConnectInfo {
	project_id: string;
	project_name: string;
	slug: string;
	api_url: string;
	region: string;
	plan: string;
	tables: { name: string; columns: { name: string; data_type: string; nullable: boolean }[] }[];
	claude_md: string;
	cursor_rules: string;
	env_template: string;
	sample_code: Record<string, string>;
}

export interface SchemaChange {
	id: string;
	project_id: string;
	action: string;
	table_name: string;
	column_name: string | null;
	detail: any;
	sql_text: string | null;
	created_at: string;
}

export const api = new EurobaseAPI();
