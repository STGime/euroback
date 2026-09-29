import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
import { z } from 'zod';
import { sqlReadOnly, type ApiClient } from '../api-client.js';
import { buildQueryString, pathSegment, type QueryTableInput } from '../query.js';

export function registerDatabaseTools(server: McpServer, getClient: () => ApiClient) {
  server.tool(
    'listTables',
    'List all tables in a project database',
    { projectId: z.string().uuid().describe('The project UUID') },
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
      projectId: z.string().uuid().describe('The project UUID'),
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

  // SQL writes (#702): a project-scoped token sends statements as asked and
  // the gateway allows or refuses them by the token's role (Read-only →
  // refused, Developer → allowed). Legacy account-wide tokens stay
  // read-only, as before (the backend wraps them in SET TRANSACTION READ
  // ONLY, so writes fail with SQLSTATE 25006). There is no server-side
  // switch: the token is the only thing that grants access.
  const writeNote =
    ' With a project token, what it may do follows its access level: Read-only tokens can\'t run SQL (use queryTable); Developer and up can read, write and change the schema. A legacy all-projects token is read-only here.';

  server.tool(
    'runSQL',
    'Execute a SINGLE SQL statement against a project database.' + writeNote +
      ' The server uses pgx\'s extended query protocol, which only runs the first statement of a multi-statement string — to prevent silent partial migrations, multi-statement input is rejected with a clear error. For migrations or any multi-statement script, use runSQLTransaction instead.',
    {
      projectId: z.string().uuid().describe('The project UUID'),
      query: z.string().describe('A single SQL statement (one query, no internal `;` between statements)'),
    },
    async ({ projectId, query }) => {
      const body: Record<string, unknown> = { sql: query };
      if (sqlReadOnly(getClient().scope)) body.read_only = true;
      const data = await getClient().post(`/platform/projects/${projectId}/data/sql`, body);
      return { content: [{ type: 'text' as const, text: JSON.stringify(data, null, 2) }] };
    }
  );

  server.tool(
    'runSQLTransaction',
    'Execute multiple SQL statements as one atomic transaction.' + writeNote +
      ' Pass each statement as its own array element (do NOT concatenate with `;`). The server runs them in order inside BEGIN...COMMIT and rolls everything back if any statement fails. Use this for migrations, schema changes with seed data, or any multi-step DDL/DML.',
    {
      projectId: z.string().uuid().describe('The project UUID'),
      statements: z.array(z.string()).describe('Array of SQL statements. Each element must be exactly one statement; do not embed multiple statements in one string.'),
      limit: z.number().int().positive().max(1000).optional().describe('Optional row cap for any SELECTs in the batch (default and max 1000)'),
    },
    async ({ projectId, statements, limit }) => {
      const body: Record<string, unknown> = { statements };
      if (limit !== undefined) body.limit = limit;
      if (sqlReadOnly(getClient().scope)) body.read_only = true;
      const data = await getClient().post(`/platform/projects/${projectId}/data/sql/transaction`, body);
      return { content: [{ type: 'text' as const, text: JSON.stringify(data, null, 2) }] };
    }
  );

  server.tool(
    'queryTable',
    'Read rows from a table, with filters, column selection, ordering and paging, or an aggregate (count / sum / avg / min / max). Works with every access level including Read-only tokens. Prefer this over runSQL for reads.',
    {
      projectId: z.string().uuid().describe('The project UUID'),
      table: z.string().describe('The table name'),
      columns: z.array(z.string()).optional().describe('Columns to return (default: all)'),
      filters: z
        .array(
          z.object({
            column: z.string(),
            op: z.enum(['eq', 'neq', 'gt', 'gte', 'lt', 'lte', 'like', 'ilike', 'in', 'is']),
            value: z.string().describe('Value; for "in" a comma-separated list; for "is": null / true / false'),
          })
        )
        .optional()
        .describe('Filters, ANDed'),
      orderBy: z.array(z.object({ column: z.string(), descending: z.boolean().optional() })).optional(),
      limit: z.number().int().positive().max(1000).optional().describe('Max rows (default 20)'),
      offset: z.number().int().nonnegative().optional(),
      aggregate: z
        .object({ fn: z.enum(['count', 'sum', 'avg', 'min', 'max']), column: z.string().optional().describe('Required except for count') })
        .optional()
        .describe('Return an aggregate instead of rows'),
    },
    async (input) => {
      const qs = buildQueryString(input as QueryTableInput);
      const data = await getClient().get(`/platform/projects/${input.projectId}/data/${pathSegment(input.table)}${qs}`);
      return { content: [{ type: 'text' as const, text: JSON.stringify(data, null, 2) }] };
    }
  );

  server.tool(
    'createTable',
    'Create a new table in the project database with specified columns and RLS',
    {
      projectId: z.string().uuid().describe('The project UUID'),
      tableName: z.string().describe('Name for the new table'),
      columns: z.string().describe('JSON array of column definitions, e.g. [{"name":"id","type":"uuid","primary_key":true,"default_value":"gen_random_uuid()"},{"name":"title","type":"text"}]'),
      rlsPreset: z.string().optional().describe('RLS preset: owner_access, public_read_owner_write, authenticated_read_owner_write, read_only, service_only, full_access, none'),
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
