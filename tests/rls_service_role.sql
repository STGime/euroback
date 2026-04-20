-- pgTAP tests for the service-role RLS bypass added in migration 000038.
--
-- The `service` value of app.end_user_role must:
--   1. Permit SELECT/INSERT/UPDATE/DELETE on `users` and `storage_objects`
--      regardless of end_user_id.
--   2. Not leak — unsetting role + id means queries return zero rows
--      (or fail gracefully) instead of raising "invalid input syntax
--      for type uuid" (the pre-000038 regression).
--
-- Prereqs: two seed users (Alice/Bob) already inserted into the tenant's
-- `users` table as they are for rls_tasks/rls_notes fixtures. This file
-- is self-contained: it inserts its own fixtures and rolls back.

BEGIN;

SELECT plan(12);

-- ----------------------------------------------------------------------
-- Seed fixtures (rolled back at end).
-- ----------------------------------------------------------------------
INSERT INTO users (id, email) VALUES
    ('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', 'alice-rls@test.local'),
    ('bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb', 'bob-rls@test.local')
ON CONFLICT (id) DO NOTHING;

INSERT INTO storage_objects (key, content_type, size_bytes, uploaded_by) VALUES
    ('alice/one.txt', 'text/plain', 10, 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa'),
    ('bob/one.txt',   'text/plain', 20, 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb')
ON CONFLICT (key) DO NOTHING;

-- ======================================================================
-- Case 1 — service role sees every user row.
-- ======================================================================
SET LOCAL app.end_user_role = 'service';

SELECT ok(
    (SELECT count(*) FROM users WHERE email IN ('alice-rls@test.local', 'bob-rls@test.local')) = 2,
    'service role sees both alice and bob'
);

SELECT ok(
    (SELECT count(*) FROM storage_objects WHERE key IN ('alice/one.txt', 'bob/one.txt')) = 2,
    'service role sees both storage_objects rows'
);

-- ======================================================================
-- Case 2 — service role can INSERT arbitrary rows (platform-admin create).
-- ======================================================================
SELECT lives_ok(
    $$INSERT INTO users (id, email) VALUES ('cccccccc-cccc-cccc-cccc-cccccccccccc', 'carol-rls@test.local')$$,
    'service role INSERT into users succeeds'
);

SELECT lives_ok(
    $$INSERT INTO storage_objects (key, content_type, size_bytes, uploaded_by) VALUES ('carol/one.txt', 'text/plain', 30, 'cccccccc-cccc-cccc-cccc-cccccccccccc')$$,
    'service role INSERT into storage_objects succeeds'
);

-- ======================================================================
-- Case 3 — clear role; queries must NOT raise "invalid input syntax".
-- Pre-000038 the policy did `id = current_setting(...)::uuid` which
-- errored on the empty-string cast. After 000038, current_end_user_id()
-- returns NULL and the WHERE clause is simply unsatisfied → zero rows.
-- ======================================================================
SET LOCAL app.end_user_role = '';
SET LOCAL app.end_user_id = '';

SELECT lives_ok(
    $$SELECT * FROM users$$,
    'SELECT on users with no end_user context does not raise'
);

SELECT ok(
    (SELECT count(*) FROM users) = 0,
    'SELECT on users with no context returns zero rows (RLS filters, no error)'
);

SELECT lives_ok(
    $$SELECT * FROM storage_objects$$,
    'SELECT on storage_objects with no context does not raise'
);

-- ======================================================================
-- Case 4 — authenticated user only sees their own row.
-- ======================================================================
SET LOCAL app.end_user_id = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa';
SET LOCAL app.end_user_role = 'authenticated';

SELECT ok(
    (SELECT count(*) FROM users WHERE id = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa') = 1,
    'alice authenticated sees her own user row'
);
SELECT ok(
    (SELECT count(*) FROM users WHERE id = 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb') = 0,
    'alice authenticated does not see bob'
);

SELECT ok(
    (SELECT count(*) FROM storage_objects WHERE uploaded_by = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa') = 1,
    'alice sees her own storage_objects row'
);
SELECT ok(
    (SELECT count(*) FROM storage_objects WHERE uploaded_by = 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb') = 0,
    'alice does not see bob storage_objects rows'
);

-- ======================================================================
-- Case 5 — service role DELETE of the rows we just inserted works,
-- proving WITH CHECK doesn't block destructive admin ops either.
-- ======================================================================
SET LOCAL app.end_user_role = 'service';
SET LOCAL app.end_user_id = '';

SELECT lives_ok(
    $$DELETE FROM users WHERE id = 'cccccccc-cccc-cccc-cccc-cccccccccccc'$$,
    'service role DELETE on users succeeds'
);

SELECT * FROM finish();
ROLLBACK;
