import { McpServer, type RegisteredTool } from '@modelcontextprotocol/sdk/server/mcp.js';
import type { ToolAnnotations } from '@modelcontextprotocol/sdk/types.js';
import type { ApiClient, TokenScope } from './api-client.js';
import { registerProjectTools } from './tools/projects.js';
import { registerDatabaseTools } from './tools/database.js';
import { registerAuthTools } from './tools/auth.js';
import { registerStorageTools } from './tools/storage.js';
import { registerVaultTools } from './tools/vault.js';
import { registerFunctionTools } from './tools/functions.js';
import { registerStatusTools } from './tools/status.js';

export function createMcpServer(getClient: () => ApiClient): McpServer {
  const scope = getClient().scope;
  const server = new McpServer({
    name: 'eurobase',
    version: '1.0.0',
  }, {
    instructions: `You are connected to Eurobase — an EU-sovereign Backend-as-a-Service platform (European alternative to Firebase/Supabase).

Available operations:
- **Projects**: List and inspect Eurobase projects
- **Database**: List tables, describe schemas, read rows (queryTable), run SQL, create tables
- **Auth**: List end-users registered in a project
- **Storage**: List files, read files (downloadFile), generate signed download URLs
- **Vault**: List, get, and set encrypted secrets
- **Functions**: List and invoke edge functions
- **Status**: Health check the API

All data is stored on EU infrastructure (Scaleway, France). No CLOUD Act exposure.

When running SQL queries, prefer SELECT for exploration. Only run INSERT/UPDATE/DELETE when the user explicitly requests data modification.

${scopeNote(scope)}`,
  });

  registerProjectTools(server, getClient);
  registerDatabaseTools(server, getClient);
  registerAuthTools(server, getClient);
  registerStorageTools(server, getClient);
  registerVaultTools(server, getClient);
  registerFunctionTools(server, getClient);
  registerStatusTools(server, getClient);

  // Attach annotations to every registered tool, however it was
  // registered (server.tool or registerTool).
  // _registeredTools is the SDK's private map (1.29, pinned with ~): if an
  // upgrade renames it, fail loudly rather than serve unannotated tools.
  const registered = (server as unknown as { _registeredTools?: Record<string, RegisteredTool> })._registeredTools;
  if (!registered || typeof registered !== 'object' || Object.keys(registered).length === 0) {
    throw new Error('@modelcontextprotocol/sdk: registered tool map not found — check the TOOL_ANNOTATIONS wiring after an SDK upgrade');
  }
  for (const [name, t] of Object.entries(registered)) {
    const annotations = TOOL_ANNOTATIONS[name];
    if (!annotations) {
      throw new Error(`MCP tool ${name} has no annotations — add it to TOOL_ANNOTATIONS`);
    }
    t.update({ title: annotations.title, annotations });
  }

  return server;
}

/**
 * MCP tool annotations: clients (Claude Code, Lovable, Cursor…) use them to
 * decide what to auto-approve — read-only tools needn't interrupt the user
 * for every call; writes and anything destructive should. Every tool must
 * have an entry (createMcpServer throws otherwise; index.ts builds one at
 * startup so a missing entry stops the pod, not every request). None reach
 * outside Eurobase (openWorldHint false), except invokeFunction, whose
 * function code may call anything.
 */
export const TOOL_ANNOTATIONS: Record<string, ToolAnnotations> = {
  // Reads.
  listProjects: { title: 'List projects', readOnlyHint: true, openWorldHint: false },
  getProject: { title: 'Get project', readOnlyHint: true, openWorldHint: false },
  listTables: { title: 'List tables', readOnlyHint: true, openWorldHint: false },
  describeTable: { title: 'Describe table', readOnlyHint: true, openWorldHint: false },
  queryTable: { title: 'Query table', readOnlyHint: true, openWorldHint: false },
  listFiles: { title: 'List files', readOnlyHint: true, openWorldHint: false },
  downloadFile: { title: 'Read file', readOnlyHint: true, openWorldHint: false },
  listFunctions: { title: 'List functions', readOnlyHint: true, openWorldHint: false },
  listSecrets: { title: 'List secrets', readOnlyHint: true, openWorldHint: false },
  // Deliberately NOT readOnlyHint: clients auto-approve read-only tools, and
  // a decrypted secret (or end users' personal data) must never reach the
  // model without the user saying yes — e.g. after a prompt injection in
  // table data asked for it.
  getSecret: { title: 'Reveal secret', readOnlyHint: false, destructiveHint: false, idempotentHint: true, openWorldHint: false },
  listUsers: { title: 'List end users', readOnlyHint: false, destructiveHint: false, idempotentHint: true, openWorldHint: false },
  healthCheck: { title: 'Health check', readOnlyHint: true, openWorldHint: false },
  // Writes.
  createTable: { title: 'Create table', readOnlyHint: false, destructiveHint: false, idempotentHint: false, openWorldHint: false },
  // Creates a link that works without sign-in: not a read.
  getSignedUrl: { title: 'Create signed URL', readOnlyHint: false, destructiveHint: false, idempotentHint: false, openWorldHint: false },
  setSecret: { title: 'Set secret', readOnlyHint: false, destructiveHint: true, idempotentHint: true, openWorldHint: false },
  // SQL can UPDATE / DELETE / DROP.
  runSQL: { title: 'Run SQL', readOnlyHint: false, destructiveHint: true, idempotentHint: false, openWorldHint: false },
  runSQLTransaction: { title: 'Run SQL transaction', readOnlyHint: false, destructiveHint: true, idempotentHint: false, openWorldHint: false },
  invokeFunction: { title: 'Invoke function', readOnlyHint: false, destructiveHint: true, idempotentHint: false, openWorldHint: true },
};

/** What this connection's token may do, for the model. */
export function scopeNote(scope: TokenScope): string {
  if (!scope.scoped) {
    return 'This token can access all of the user\'s Eurobase projects (a legacy token); SQL through it is read-only. Suggest creating a project token (console → Account → Personal Access Tokens): one project, with the access it needs.';
  }
  const access = { viewer: 'read-only (tables, rows, files; no SQL, no changes)', developer: 'developer (read, write, SQL, create and change tables, invoke functions)', admin: 'admin (developer access plus secrets, end users and API keys)' }[scope.role ?? 'viewer'];
  return `This token works for one project only: ${scope.projectId}, with ${access} access. Other projects are not reachable with it.`;
}
