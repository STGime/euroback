import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
import { z } from 'zod';
import type { ApiClient } from '../api-client.js';

export function registerVaultTools(server: McpServer, getClient: () => ApiClient) {
  server.tool(
    'listSecrets',
    'List secret names and descriptions in the project vault (values are not returned)',
    { projectId: z.string().describe('The project UUID') },
    async ({ projectId }) => {
      const data = await getClient().get(`/platform/projects/${projectId}/vault`);
      return { content: [{ type: 'text' as const, text: JSON.stringify(data, null, 2) }] };
    }
  );

  server.tool(
    'getSecret',
    'Get the decrypted value of a secret from the project vault',
    {
      projectId: z.string().describe('The project UUID'),
      name: z.string().describe('The secret name'),
    },
    async ({ projectId, name }) => {
      const data = await getClient().get(`/platform/projects/${projectId}/vault/${encodeURIComponent(name)}`);
      return { content: [{ type: 'text' as const, text: JSON.stringify(data, null, 2) }] };
    }
  );

  server.tool(
    'setSecret',
    'Create or update a secret in the project vault',
    {
      projectId: z.string().describe('The project UUID'),
      name: z.string().describe('The secret name'),
      value: z.string().describe('The secret value'),
      description: z.string().optional().describe('Optional description'),
    },
    async ({ projectId, name, value, description }) => {
      const data = await getClient().post(`/platform/projects/${projectId}/vault`, {
        name,
        value,
        description: description || '',
      });
      return { content: [{ type: 'text' as const, text: JSON.stringify(data, null, 2) }] };
    }
  );
}
