import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
import type { ApiClient } from '../api-client.js';

export function registerStatusTools(server: McpServer, getClient: () => ApiClient) {
  server.tool(
    'healthCheck',
    'Check if the Eurobase API is healthy and responsive',
    {},
    async () => {
      const start = Date.now();
      const data = await getClient().get('/health');
      const latency = Date.now() - start;
      return {
        content: [{
          type: 'text' as const,
          text: JSON.stringify({ ...(data as object), latency_ms: latency }, null, 2),
        }],
      };
    }
  );
}
