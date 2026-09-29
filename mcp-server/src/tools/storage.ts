import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
import { z } from 'zod';
import type { ApiClient } from '../api-client.js';
import { downloadKind, storageKeyPath } from '../query.js';

/** Largest file downloadFile returns as content (keeps the model's context sane). */
const DOWNLOAD_CAP = 5 * 1024 * 1024;

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
    'downloadFile',
    'Read a file from project storage (as in the console storage browser): images come back as images, text files (JSON, CSV, Markdown…) as text, anything else as metadata only. Files over 5 MB are cut off. Works with Read-only tokens.',
    {
      projectId: z.string().describe('The project UUID'),
      key: z.string().describe('The file key/path'),
    },
    async ({ projectId, key }) => {
      const raw = await getClient().getRaw(`/platform/projects/${projectId}/storage/${storageKeyPath(key)}`, DOWNLOAD_CAP);
      const kind = downloadKind(raw.contentType);
      const meta = { key, content_type: raw.contentType, bytes_read: raw.bytes.length, truncated: raw.truncated };
      if (kind === 'image' && !raw.truncated) {
        return {
          content: [
            { type: 'image' as const, data: Buffer.from(raw.bytes).toString('base64'), mimeType: raw.contentType.split(';')[0].trim() },
            { type: 'text' as const, text: JSON.stringify(meta) },
          ],
        };
      }
      if (kind === 'text') {
        const text = new TextDecoder().decode(raw.bytes);
        return { content: [{ type: 'text' as const, text: (raw.truncated ? `[first ${DOWNLOAD_CAP} bytes]\n` : '') + text }] };
      }
      return { content: [{ type: 'text' as const, text: JSON.stringify({ ...meta, note: 'binary file — not returned as content' }, null, 2) }] };
    }
  );

  server.tool(
    'getSignedUrl',
    'Generate a signed download URL for a file in project storage (a shareable link that works without sign-in — needs Developer access)',
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
