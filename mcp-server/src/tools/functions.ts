import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
import { z } from 'zod';
import type { ApiClient } from '../api-client.js';

export function registerFunctionTools(server: McpServer, getClient: () => ApiClient) {
  server.tool(
    'listFunctions',
    'List edge functions deployed in a project',
    { projectId: z.string().describe('The project UUID') },
    async ({ projectId }) => {
      const data = await getClient().get(`/platform/projects/${projectId}/functions`);
      return { content: [{ type: 'text' as const, text: JSON.stringify(data, null, 2) }] };
    }
  );

  server.tool(
    'invokeFunction',
    'Invoke an edge function by name with an optional JSON body',
    {
      functionName: z.string().describe('The function name'),
      body: z.string().optional().describe('JSON request body'),
    },
    async ({ functionName, body }) => {
      const parsed = body ? JSON.parse(body) : undefined;
      const data = await getClient().post(`/v1/functions/${encodeURIComponent(functionName)}`, parsed);
      return { content: [{ type: 'text' as const, text: JSON.stringify(data, null, 2) }] };
    }
  );
}
