import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
import type { ApiClient } from './api-client.js';
import { registerProjectTools } from './tools/projects.js';
import { registerDatabaseTools } from './tools/database.js';
import { registerAuthTools } from './tools/auth.js';
import { registerStorageTools } from './tools/storage.js';
import { registerVaultTools } from './tools/vault.js';
import { registerFunctionTools } from './tools/functions.js';
import { registerStatusTools } from './tools/status.js';

export function createMcpServer(getClient: () => ApiClient): McpServer {
  const server = new McpServer({
    name: 'eurobase',
    version: '1.0.0',
  }, {
    instructions: `You are connected to Eurobase — an EU-sovereign Backend-as-a-Service platform (European alternative to Firebase/Supabase).

Available operations:
- **Projects**: List and inspect Eurobase projects
- **Database**: List tables, describe schemas, run SQL queries, create tables
- **Auth**: List end-users registered in a project
- **Storage**: List files, generate signed download URLs
- **Vault**: List, get, and set encrypted secrets
- **Functions**: List and invoke edge functions
- **Status**: Health check the API

All data is stored on EU infrastructure (Scaleway, France). No CLOUD Act exposure.

When running SQL queries, prefer SELECT for exploration. Only run INSERT/UPDATE/DELETE when the user explicitly requests data modification.`,
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
