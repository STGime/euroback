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
    'Execute a SINGLE SQL statement against a project database. The server uses pgx\'s extended query protocol, which only runs the first statement of a multi-statement string — to prevent silent partial migrations, multi-statement input is rejected with a clear error. For migrations or any multi-statement script, use runSQLTransaction instead.',
    {
      projectId: z.string().describe('The project UUID'),
      query: z.string().describe('A single SQL statement (one query, no internal `;` between statements)'),
    },
    async ({ projectId, query }) => {
      const data = await getClient().post(`/platform/projects/${projectId}/data/sql`, { sql: query });
      return { content: [{ type: 'text' as const, text: JSON.stringify(data, null, 2) }] };
    }
  );

  server.tool(
    'runSQLTransaction',
    'Execute multiple SQL statements as one atomic transaction. Pass each statement as its own array element (do NOT concatenate with `;`). The server runs them in order inside BEGIN...COMMIT and rolls everything back if any statement fails. Use this for migrations, schema changes with seed data, or any multi-step DDL/DML.',
    {
      projectId: z.string().describe('The project UUID'),
      statements: z.array(z.string()).describe('Array of SQL statements. Each element must be exactly one statement; do not embed multiple statements in one string.'),
      limit: z.number().int().positive().max(1000).optional().describe('Optional row cap for any SELECTs in the batch (default and max 1000)'),
    },
    async ({ projectId, statements, limit }) => {
      const body: Record<string, unknown> = { statements };
      if (limit !== undefined) body.limit = limit;
      const data = await getClient().post(`/platform/projects/${projectId}/data/sql/transaction`, body);
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
