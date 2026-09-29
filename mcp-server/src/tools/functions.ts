import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
import { z } from 'zod';
import type { ApiClient } from '../api-client.js';
import { pathSegment } from '../query.js';

export function registerFunctionTools(server: McpServer, getClient: () => ApiClient) {
  server.tool(
    'listFunctions',
    'List edge functions deployed in a project',
    { projectId: z.string().uuid().describe('The project UUID') },
    async ({ projectId }) => {
      const data = await getClient().get(`/platform/projects/${projectId}/functions`);
      return { content: [{ type: 'text' as const, text: JSON.stringify(data, null, 2) }] };
    }
  );

  server.tool(
    'invokeFunction',
    'Invoke a deployed edge function (as the console\'s Test button does) and return its status, headers and body. Needs Developer access.',
    {
      projectId: z.string().uuid().describe('The project UUID'),
      functionName: z.string().describe('The function name'),
      method: z.enum(['GET', 'POST', 'PUT', 'PATCH', 'DELETE']).optional().describe('HTTP method (default POST)'),
      body: z.string().optional().describe('Request body (e.g. JSON text)'),
      query: z.string().optional().describe('Query string without "?"'),
    },
    async ({ projectId, functionName, method, body, query }) => {
      const data = await getClient().post(
        `/platform/projects/${projectId}/functions/${pathSegment(functionName)}/test`,
        { method: method ?? 'POST', body: body ?? '', query: query ?? '' }
      );
      return { content: [{ type: 'text' as const, text: JSON.stringify(data, null, 2) }] };
    }
  );
}
