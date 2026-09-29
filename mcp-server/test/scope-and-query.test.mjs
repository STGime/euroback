// Unit tests for the MCP server's token-scope handling and query building
// (#702). Run: npm test (builds first).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import { scopeFromProfile, sqlReadOnly, explain } from '../dist/api-client.js';
import { buildQueryString, storageKeyPath, downloadKind } from '../dist/query.js';
import { scopeNote } from '../dist/server.js';

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
  assert.match(explain('x', 404, undefined, viewer), new RegExp(P));
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

test('storage keys and download kinds', () => {
  assert.equal(storageKeyPath('/avatars/a b.png'), 'avatars/a%20b.png');
  assert.equal(storageKeyPath('x/../y'), 'x/../y'.split('/').map(encodeURIComponent).join('/'));
  assert.equal(downloadKind('image/png'), 'image');
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
    for (const m of readFileSync(file, 'utf8').matchAll(/process\.env\.([A-Z0-9_]+)/g)) {
      assert.ok(allowed.has(m[1]), `${file.pathname} reads process.env.${m[1]}`);
    }
  }
});
