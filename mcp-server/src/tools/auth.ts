import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
import { z } from 'zod';
import type { ApiClient } from '../api-client.js';

export function registerAuthTools(server: McpServer, getClient: () => ApiClient) {
  server.tool(
    'listUsers',
    'List end-users registered in a project',
    { projectId: z.string().describe('The project UUID') },
    async ({ projectId }) => {
      const data = await getClient().get(`/platform/projects/${projectId}/users`);
      return { content: [{ type: 'text' as const, text: JSON.stringify(data, null, 2) }] };
    }
  );
}
