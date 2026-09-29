import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
import { z } from 'zod';
import type { ApiClient } from '../api-client.js';
import { downloadKind, isExportArchiveKey, storageKeyPath } from '../query.js';

/** Largest image returned as an image (the model's per-image limit, after base64). */
const IMAGE_CAP = 3_500_000;
/** Most text returned (keeps the model's context usable). */
const TEXT_CAP = 512 * 1024;

export function registerStorageTools(server: McpServer, getClient: () => ApiClient) {
  server.tool(
    'listFiles',
    'List files in project storage, optionally filtered by prefix',
    {
      projectId: z.string().uuid().describe('The project UUID'),
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
    'Read a file from project storage (as in the console storage browser): PNG / JPEG / GIF / WebP images (up to 3.5 MB) come back as images, text files (JSON, CSV, Markdown…) as text (first 512 KB), anything else as metadata only. Works with Read-only tokens.',
    {
      projectId: z.string().uuid().describe('The project UUID'),
      key: z.string().min(1).describe('The file key/path'),
    },
    async ({ projectId, key }) => {
      if (isExportArchiveKey(key)) return exportRefusal();
      const raw = await getClient().getRaw(`/platform/projects/${projectId}/storage/${storageKeyPath(key)}`, IMAGE_CAP);
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
        const cut = raw.truncated || raw.bytes.length > TEXT_CAP;
        const text = new TextDecoder().decode(raw.bytes.subarray(0, TEXT_CAP));
        return { content: [{ type: 'text' as const, text: (cut ? `[first ${TEXT_CAP} bytes]\n` : '') + text }] };
      }
      return { content: [{ type: 'text' as const, text: JSON.stringify({ ...meta, note: 'binary file — not returned as content' }, null, 2) }] };
    }
  );

  server.tool(
    'getSignedUrl',
    'Generate a signed download URL for a file in project storage (a shareable link that works without sign-in — needs Developer access)',
    {
      projectId: z.string().uuid().describe('The project UUID'),
      key: z.string().describe('The file key/path'),
      expiresIn: z.number().optional().describe('URL expiry in seconds (default 3600)'),
    },
    async ({ projectId, key, expiresIn }) => {
      if (isExportArchiveKey(key)) return exportRefusal();
      const body: Record<string, unknown> = { key, operation: 'download' };
      if (expiresIn) body.expires_in = expiresIn;
      const data = await getClient().post(`/platform/projects/${projectId}/storage/signed-url`, body);
      return { content: [{ type: 'text' as const, text: JSON.stringify(data, null, 2) }] };
    }
  );
}

function exportRefusal() {
  return {
    content: [{ type: 'text' as const, text: 'Compliance export archives (exports/…) are full project dumps and are not available through MCP. Download them in the Eurobase console → Compliance → Data Export.' }],
    isError: true,
  };
}
