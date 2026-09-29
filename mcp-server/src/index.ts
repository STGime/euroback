import express from 'express';
import { StreamableHTTPServerTransport } from '@modelcontextprotocol/sdk/server/streamableHttp.js';
import { ApiClient } from './api-client.js';
import { createMcpServer } from './server.js';

// Build one server before listening: a tool without annotations or an SDK
// upgrade that breaks their wiring then fails the pod's startup instead of
// every /mcp request (the probes only hit /health).
createMcpServer(() => ({ scope: { scoped: false } }) as unknown as ApiClient);

const app = express();
app.use(express.json());

const PORT = parseInt(process.env.PORT || '3100', 10);

// Health check
app.get('/health', (_req, res) => {
  res.json({ status: 'ok', service: 'eurobase-mcp-server' });
});

// MCP endpoint — Streamable HTTP transport
app.post('/mcp', async (req, res) => {
  const authHeader = req.headers.authorization;
  if (!authHeader?.startsWith('Bearer ')) {
    res.status(401).json({ error: 'Missing or invalid Authorization header. Use: Bearer <platform-token>' });
    return;
  }

  const token = authHeader.slice(7);
  const apiClient = new ApiClient(token);

  // Validate the token
  const valid = await apiClient.validateToken();
  if (!valid) {
    res.status(401).json({ error: 'Invalid or expired platform token' });
    return;
  }

  const server = createMcpServer(() => apiClient);
  const transport = new StreamableHTTPServerTransport({ sessionIdGenerator: undefined });

  res.on('close', () => {
    transport.close();
    server.close();
  });

  await server.connect(transport);
  await transport.handleRequest(req, res, req.body);
});

// Handle GET and DELETE for session management (required by Streamable HTTP spec)
app.get('/mcp', async (_req, res) => {
  res.status(405).json({ error: 'Method not allowed. Use POST for MCP requests.' });
});

app.delete('/mcp', async (_req, res) => {
  res.status(405).json({ error: 'Method not allowed.' });
});

app.listen(PORT, () => {
  console.log(`Eurobase MCP server listening on port ${PORT}`);
});
