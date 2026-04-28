import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
import { z } from 'zod';
import type { ApiClient } from '../api-client.js';

export function registerProjectTools(server: McpServer, getClient: () => ApiClient) {
  server.tool(
    'listProjects',
    'List all Eurobase projects accessible to the authenticated user',
    {},
    async () => {
      const data = await getClient().get('/v1/tenants');
      return { content: [{ type: 'text' as const, text: JSON.stringify(data, null, 2) }] };
    }
  );

  server.tool(
    'getProject',
    'Get details of a specific Eurobase project by ID',
    { projectId: z.string().describe('The project UUID') },
    async ({ projectId }) => {
      const projects = await getClient().get('/v1/tenants') as any[];
      const project = projects.find((p: any) => p.id === projectId);
      if (!project) {
        return { content: [{ type: 'text' as const, text: `Project ${projectId} not found` }], isError: true };
      }
      return { content: [{ type: 'text' as const, text: JSON.stringify(project, null, 2) }] };
    }
  );
}
