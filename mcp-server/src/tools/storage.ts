import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
import { z } from 'zod';
import type { ApiClient } from '../api-client.js';

export function registerStorageTools(server: McpServer, getClient: () => ApiClient) {
  server.tool(
    'listFiles',
    'List files in project storage, optionally filtered by prefix',
    {
      projectId: z.string().describe('The project UUID'),
      prefix: z.string().optional().describe('Filter files by key prefix'),
    },
    async ({ projectId, prefix }) => {
      const query = prefix ? `?prefix=${encodeURIComponent(prefix)}` : '';
      const data = await getClient().get(`/platform/projects/${projectId}/storage${query}`);
      return { content: [{ type: 'text' as const, text: JSON.stringify(data, null, 2) }] };
    }
  );

  server.tool(
    'getSignedUrl',
    'Generate a signed download URL for a file in project storage',
    {
      projectId: z.string().describe('The project UUID'),
      key: z.string().describe('The file key/path'),
      expiresIn: z.number().optional().describe('URL expiry in seconds (default 3600)'),
    },
    async ({ projectId, key, expiresIn }) => {
      const body: Record<string, unknown> = { key, operation: 'download' };
      if (expiresIn) body.expires_in = expiresIn;
      const data = await getClient().post(`/platform/projects/${projectId}/storage/signed-url`, body);
      return { content: [{ type: 'text' as const, text: JSON.stringify(data, null, 2) }] };
    }
  );
}
