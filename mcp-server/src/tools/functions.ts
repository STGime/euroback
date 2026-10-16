import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
import { z } from 'zod';
import { ApiError, type ApiClient } from '../api-client.js';
import { pathSegment } from '../query.js';

const fnPath = (projectId: string, name: string) =>
  `/platform/projects/${projectId}/functions/${pathSegment(name)}`;

const json = (data: unknown) => ({ content: [{ type: 'text' as const, text: JSON.stringify(data, null, 2) }] });

/**
 * A function's env vars are its secrets (decrypted by the gateway, given to
 * the code as ctx.env): never hand their values to the model — only the
 * names. Revealing a secret needs an approved tool (getSecret), and these
 * function tools are auto-approvable.
 */
export function redactFunction(fn: unknown): unknown {
  if (!fn || typeof fn !== 'object' || Array.isArray(fn)) return fn;
  const { env_vars, ...rest } = fn as Record<string, unknown>;
  if (env_vars && typeof env_vars === 'object') {
    return { ...rest, env_var_names: Object.keys(env_vars as Record<string, unknown>).sort() };
  }
  return rest;
}

/** The create lost a race with another create of the same name. */
const isDuplicate = (err: unknown) =>
  err instanceof ApiError && err.status === 400 && /duplicate key|already exists/i.test(err.message);

export function registerFunctionTools(server: McpServer, getClient: () => ApiClient) {
  server.tool(
    'listFunctions',
    'List edge functions deployed in a project',
    { projectId: z.string().uuid().describe('The project UUID') },
    async ({ projectId }) => {
      const data = await getClient().get(`/platform/projects/${projectId}/functions`);
      return json(data);
    }
  );

  server.tool(
    'getFunction',
    'Get an edge function: its code, version, status, whether it requires a signed-in user (verify_jwt) and the names of its environment variables (values are never shown).',
    {
      projectId: z.string().uuid().describe('The project UUID'),
      functionName: z.string().describe('The function name'),
    },
    async ({ projectId, functionName }) => json(redactFunction(await getClient().get(fnPath(projectId, functionName))))
  );

  // Create-or-update in one tool (#75), like the CLI's `functions deploy`:
  // agents shouldn't have to know whether the function exists yet.
  server.tool(
    'deployFunction',
    'Deploy an edge function: creates it if it doesn\'t exist, otherwise replaces its code (the previous code is kept as a version; see listFunctionVersions / rollbackFunction). ' +
      'Code is TypeScript or JavaScript exporting the handler, e.g. `export default async (req, ctx) => Response.json({ ok: true })`; ctx gives db.sql, vault, storage and user (see docs, Edge Functions). Types are stripped on deploy, not checked. ' +
      'Names start with a lowercase letter and use lowercase letters, digits, - and _ (max 63). Needs Developer access.',
    {
      projectId: z.string().uuid().describe('The project UUID'),
      functionName: z.string().describe('The function name'),
      code: z.string().min(1).describe('The full source code of the function'),
      verifyJwt: z
        .boolean()
        .optional()
        .describe('Require a signed-in end user to call it. New functions default to true; on update, omit to keep the current setting. Set false only for public endpoints such as webhooks.'),
    },
    async ({ projectId, functionName, code, verifyJwt }) => {
      const client = getClient();
      const path = fnPath(projectId, functionName);
      let exists = true;
      try {
        await client.get(path);
      } catch (err) {
        if (!(err instanceof ApiError && err.status === 404)) throw err;
        exists = false;
      }
      const update = async () => {
        const body: Record<string, unknown> = { code };
        if (verifyJwt !== undefined) body.verify_jwt = verifyJwt;
        return json({ action: 'updated', function: redactFunction(await client.put(path, body)) });
      };
      if (exists) return update();
      try {
        const created = await client.post(`/platform/projects/${projectId}/functions`, {
          name: functionName,
          code,
          verify_jwt: verifyJwt ?? true,
        });
        return json({ action: 'created', function: redactFunction(created) });
      } catch (err) {
        // Created by someone else between our GET and POST: update instead.
        if (isDuplicate(err)) return update();
        throw err;
      }
    }
  );

  server.tool(
    'deleteFunction',
    'Delete an edge function together with its versions, triggers and invocation logs. Needs Developer access.',
    {
      projectId: z.string().uuid().describe('The project UUID'),
      functionName: z.string().describe('The function name'),
    },
    async ({ projectId, functionName }) => json(await getClient().del(fnPath(projectId, functionName)))
  );

  server.tool(
    'getFunctionLogs',
    'Recent invocations of an edge function, newest first: status, duration, error and the lines it logged.',
    {
      projectId: z.string().uuid().describe('The project UUID'),
      functionName: z.string().describe('The function name'),
      limit: z.number().int().positive().max(100).optional().describe('How many invocations (default 50, max 100)'),
    },
    async ({ projectId, functionName, limit }) =>
      json(await getClient().get(`${fnPath(projectId, functionName)}/logs?limit=${limit ?? 50}`))
  );

  server.tool(
    'listFunctionVersions',
    'List the earlier versions of an edge function (version number, code, date), for rollbackFunction.',
    {
      projectId: z.string().uuid().describe('The project UUID'),
      functionName: z.string().describe('The function name'),
    },
    async ({ projectId, functionName }) => json(await getClient().get(`${fnPath(projectId, functionName)}/versions`))
  );

  server.tool(
    'rollbackFunction',
    'Restore an earlier version of an edge function\'s code (from listFunctionVersions). Needs Developer access.',
    {
      projectId: z.string().uuid().describe('The project UUID'),
      functionName: z.string().describe('The function name'),
      version: z.number().int().positive().describe('The version number to restore'),
    },
    async ({ projectId, functionName, version }) =>
      json(redactFunction(await getClient().post(`${fnPath(projectId, functionName)}/rollback`, { version })))
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
        `${fnPath(projectId, functionName)}/test`,
        { method: method ?? 'POST', body: body ?? '', query: query ?? '' }
      );
      return json(data);
    }
  );
}
