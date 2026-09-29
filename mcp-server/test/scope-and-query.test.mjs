// Unit tests for the MCP server's token-scope handling and query building
// (#702). Run: npm test (builds first).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import { scopeFromProfile, sqlReadOnly, explain } from '../dist/api-client.js';
import { buildQueryString, storageKeyPath, downloadKind, pathSegment, isExportArchiveKey, readsInternalTable } from '../dist/query.js';
import { scopeNote, createMcpServer, TOOL_ANNOTATIONS } from '../dist/server.js';

const P = '0b1e5d6a-0000-4000-8000-000000000001';

test('token scope from the profile', () => {
  assert.deepEqual(scopeFromProfile({ token: { scoped: true, project_id: P, role: 'developer' } }), { scoped: true, projectId: P, role: 'developer' });
  assert.deepEqual(scopeFromProfile({ token: { scoped: false } }), { scoped: false });
  assert.deepEqual(scopeFromProfile({}), { scoped: false }); // console session / older gateway
  assert.equal(scopeFromProfile({ token: { scoped: true, project_id: P, role: 'weird' } }).role, 'viewer'); // unknown → least
});

test('SQL: legacy tokens stay read-only, scoped tokens let the gateway decide', () => {
  assert.equal(sqlReadOnly({ scoped: false }), true);
  assert.equal(sqlReadOnly({ scoped: true, projectId: P, role: 'viewer' }), false);
  assert.equal(sqlReadOnly({ scoped: true, projectId: P, role: 'developer' }), false);
});

test('refusals are explained', () => {
  const viewer = { scoped: true, projectId: P, role: 'viewer' };
  assert.match(explain('x', 403, undefined, viewer), /read-only.*queryTable.*Developer token/s);
  assert.match(explain('x', 403, 'pat_out_of_scope', viewer), /one project/);
  // A 404 on another project hints at the token's project; a missing file /
  // table in its own project doesn't.
  assert.match(explain('x', 404, undefined, viewer, '/platform/projects/other/data/t'), new RegExp(P));
  assert.equal(explain('x', 404, undefined, viewer, `/platform/projects/${P}/storage/missing.txt`), 'x');
  assert.equal(explain('x', 403, undefined, { scoped: false }), 'x');
});

test('scope note for the model', () => {
  assert.match(scopeNote({ scoped: false }), /all of the user's Eurobase projects/);
  assert.match(scopeNote({ scoped: true, projectId: P, role: 'viewer' }), /read-only/);
});

test('queryTable query string', () => {
  assert.equal(buildQueryString({ projectId: P, table: 't' }), '');
  const qs = new URLSearchParams(
    buildQueryString({
      projectId: P,
      table: 'users',
      columns: ['id', 'email'],
      filters: [
        { column: 'created_at', op: 'gte', value: '2026-09-21' },
        { column: 'status', op: 'in', value: 'active,pending' },
      ],
      orderBy: [{ column: 'created_at', descending: true }],
      limit: 20,
      offset: 40,
    }).slice(1)
  );
  assert.equal(qs.get('select'), 'id,email');
  assert.equal(qs.get('created_at'), 'gte.2026-09-21');
  assert.equal(qs.get('status'), 'in.(active,pending)');
  assert.equal(qs.get('order'), 'created_at.desc');
  assert.equal(qs.get('limit'), '20');
  assert.equal(qs.get('offset'), '40');
  assert.equal(new URLSearchParams(buildQueryString({ projectId: P, table: 't', aggregate: { fn: 'count' } }).slice(1)).get('aggregate'), 'count');
  assert.equal(new URLSearchParams(buildQueryString({ projectId: P, table: 't', aggregate: { fn: 'sum', column: 'amount' } }).slice(1)).get('aggregate'), 'sum:amount');
});

test('path segments for names', () => {
  for (const bad of ['', '.', '..', 'a/b', 'a\\b']) {
    assert.throws(() => pathSegment(bad), /invalid name/, bad);
  }
  assert.equal(pathSegment('my table'), 'my%20table');
  assert.equal(pathSegment('a..b'), 'a..b');
});

test('filters on reserved parameter names are refused', () => {
  for (const col of ['select', 'order', 'limit', 'offset', 'aggregate']) {
    assert.throws(() => buildQueryString({ projectId: P, table: 't', filters: [{ column: col, op: 'eq', value: '1' }] }), /clashes/);
  }
});

test('export archives are off limits', () => {
  for (const k of ['exports/p/x.zip', '/exports/p/x.zip', 'EXPORTS/p/x.zip']) assert.ok(isExportArchiveKey(k), k);
  for (const k of ['exportsx/a', 'avatars/exports/a', 'report-exports.csv']) assert.ok(!isExportArchiveKey(k), k);
});

test('queryTable refuses the platform tables (end users need an approved tool)', () => {
  for (const t of ['users', 'user_identities', 'refresh_tokens', 'email_tokens', 'vault_secrets', 'storage_objects', 'storage_shared_prefixes']) {
    assert.equal(readsInternalTable(t), t);
  }
  assert.equal(readsInternalTable('notes', ['id', 'owner:users(email)']), 'users');
  assert.equal(readsInternalTable('notes', ['*', 'users!inner(*)']), 'users');
  assert.equal(readsInternalTable('notes', ['a:vault_secrets ( secret )']), 'vault_secrets');
  assert.equal(readsInternalTable('notes', ['id', 'users', 'tags(name)']), undefined);
  assert.equal(readsInternalTable('app_users'), undefined);
  // Same set as the gateway's internal tables.
  const go = readFileSync(new URL('../../internal/query/internal_tables.go', import.meta.url), 'utf8');
  const block = go.match(/var internalTenantTables = map\[string\]bool\{([^}]*)\}/)[1];
  for (const [, t] of block.matchAll(/"([a-z_]+)":/g)) assert.equal(readsInternalTable(t), t, t);
});

test('storage keys and download kinds', () => {
  assert.equal(storageKeyPath('/avatars/a b.png'), 'avatars/a%20b.png');
  // No walking to other API routes: fetch would resolve . / .. before the gateway sees them.
  for (const bad of ['x/../y', '../connection', '../../other/vault/X', 'a/./b', '', 'a//b', 'a\\..\\b']) {
    assert.throws(() => storageKeyPath(bad), /invalid storage key/, bad);
  }
  assert.equal(storageKeyPath('a.b/c..d.txt'), 'a.b/c..d.txt');
  assert.equal(downloadKind('image/png'), 'image');
  assert.equal(downloadKind('image/tiff'), 'binary'); // not accepted by clients as an image
  assert.equal(downloadKind('image/svg+xml'), 'text');
  assert.equal(downloadKind('application/json; charset=utf-8'), 'text');
  assert.equal(downloadKind('text/csv'), 'text');
  assert.equal(downloadKind('application/zip'), 'binary');
});

// The MCP server has no access of its own: the token decides (#702).
test('no credentials in the MCP server', () => {
  const manifest = readFileSync(new URL('../../deploy/k8s/mcp-server.yaml', import.meta.url), 'utf8');
  for (const forbidden of ['envFrom', 'secretRef', 'secretKeyRef']) {
    assert.ok(!manifest.includes(forbidden), `mcp-server.yaml must not use ${forbidden}`);
  }
  const src = new URL('../src/', import.meta.url);
  const allowed = new Set(['EUROBASE_PLATFORM_URL', 'PORT']);
  const walk = (dir) =>
    readdirSync(dir, { withFileTypes: true }).flatMap((e) =>
      e.isDirectory() ? walk(new URL(e.name + '/', dir)) : e.name.endsWith('.ts') ? [new URL(e.name, dir)] : []
    );
  for (const file of walk(src)) {
    const text = readFileSync(file, 'utf8');
    for (const m of text.matchAll(/process\.env\.([A-Z0-9_]+)/g)) {
      assert.ok(allowed.has(m[1]), `${file.pathname} reads process.env.${m[1]}`);
    }
    assert.ok(!/process\.env\s*\[/.test(text), `${file.pathname} indexes process.env`);
    assert.ok(!/=\s*process\.env\s*[;\n]/.test(text), `${file.pathname} copies process.env`);
  }
});

// Every tool carries annotations (clients use them to decide what to
// auto-approve): reads are readOnlyHint, SQL and function calls destructive.
test('tool annotations', () => {
  const server = createMcpServer(() => ({ scope: { scoped: false } }));
  const tools = server._registeredTools;
  const names = Object.keys(tools);
  assert.ok(names.length >= 18, `tools: ${names.length}`);
  for (const name of names) {
    assert.ok(tools[name].annotations, `${name} has no annotations`);
    assert.deepEqual(tools[name].annotations, TOOL_ANNOTATIONS[name], name);
  }
  for (const r of ['listTables', 'queryTable', 'downloadFile', 'listProjects']) assert.equal(tools[r].annotations.readOnlyHint, true, r);
  for (const w of ['runSQL', 'runSQLTransaction', 'invokeFunction', 'setSecret']) {
    assert.equal(tools[w].annotations.readOnlyHint, false, w);
    assert.equal(tools[w].annotations.destructiveHint, true, w);
  }
  // Secrets and end users' data always need the user's OK.
  for (const p of ['getSecret', 'listUsers', 'getSignedUrl']) assert.equal(tools[p].annotations.readOnlyHint, false, p);
  // No stale entries: the table and the registered tools match.
  assert.deepEqual(Object.keys(TOOL_ANNOTATIONS).sort(), names.sort());
  assert.equal(tools.runSQL.title, 'Run SQL');
});
