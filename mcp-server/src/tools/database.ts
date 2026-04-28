import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
import { z } from 'zod';
import type { ApiClient } from '../api-client.js';

export function registerDatabaseTools(server: McpServer, getClient: () => ApiClient) {
  server.tool(
    'listTables',
    'List all tables in a project database',
    { projectId: z.string().describe('The project UUID') },
    async ({ projectId }) => {
      const data = await getClient().get(`/platform/projects/${projectId}/schema`);
      const tables = (data as any[]).map((t: any) => t.table_name || t.name);
      return { content: [{ type: 'text' as const, text: JSON.stringify(tables, null, 2) }] };
    }
  );

  server.tool(
    'describeTable',
    'Get the schema (columns, types, constraints) of a specific table',
    {
      projectId: z.string().describe('The project UUID'),
      tableName: z.string().describe('The table name'),
    },
    async ({ projectId, tableName }) => {
      const data = await getClient().get(`/platform/projects/${projectId}/schema`) as any[];
      const table = data.find((t: any) => (t.table_name || t.name) === tableName);
      if (!table) {
        return { content: [{ type: 'text' as const, text: `Table ${tableName} not found` }], isError: true };
      }
      return { content: [{ type: 'text' as const, text: JSON.stringify(table, null, 2) }] };
    }
  );

  server.tool(
    'runSQL',
    'Execute a SQL query against a project database. Use with caution — can modify data.',
    {
      projectId: z.string().describe('The project UUID'),
      query: z.string().describe('The SQL query to execute'),
    },
    async ({ projectId, query }) => {
      const data = await getClient().post(`/platform/projects/${projectId}/data/sql`, { query });
      return { content: [{ type: 'text' as const, text: JSON.stringify(data, null, 2) }] };
    }
  );

  server.tool(
    'createTable',
    'Create a new table in the project database with specified columns and RLS',
    {
      projectId: z.string().describe('The project UUID'),
      tableName: z.string().describe('Name for the new table'),
      columns: z.string().describe('JSON array of column definitions, e.g. [{"name":"id","type":"uuid","primary_key":true,"default_value":"gen_random_uuid()"},{"name":"title","type":"text"}]'),
      rlsPreset: z.string().optional().describe('RLS preset: owner_access, public_read_owner_write, authenticated_read_owner_write, full_access, read_only, none'),
    },
    async ({ projectId, tableName, columns, rlsPreset }) => {
      const body: Record<string, unknown> = {
        name: tableName,
        columns: JSON.parse(columns),
      };
      if (rlsPreset) body.rls_preset = rlsPreset;
      const data = await getClient().post(`/platform/projects/${projectId}/schema/tables`, body);
      return { content: [{ type: 'text' as const, text: JSON.stringify(data, null, 2) }] };
    }
  );
}
