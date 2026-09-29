import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
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

  return server;
}

/** What this connection's token may do, for the model. */
export function scopeNote(scope: TokenScope): string {
  if (!scope.scoped) {
    return 'This token can access all of the user\'s Eurobase projects (a legacy token); SQL through it is read-only. Suggest creating a project token (console → Account → Personal Access Tokens): one project, with the access it needs.';
  }
  const access = { viewer: 'read-only (tables, rows, files; no SQL, no changes)', developer: 'developer (read, write, SQL, create and change tables, deploy functions)', admin: 'admin (developer access plus secrets, API keys and project settings)' }[scope.role ?? 'viewer'];
  return `This token works for one project only: ${scope.projectId}, with ${access} access. Other projects are not reachable with it.`;
}
