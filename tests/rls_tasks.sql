-- pgTAP tests for RLS policies on the tasks table
-- Policy: owner_access — users can only CRUD their own rows

BEGIN;

SELECT plan(6);

-- ================================================================
-- Test as Alice
-- ================================================================
SET LOCAL app.end_user_id = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa';
SET LOCAL app.end_user_role = 'authenticated';

-- Test 1: Alice can see her own tasks
SELECT ok(
    (SELECT count(*) FROM tasks WHERE user_id = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa') = 2,
    'Alice can see her 2 tasks'
);

-- Test 2: Alice cannot see Bob's tasks
SELECT ok(
    (SELECT count(*) FROM tasks WHERE user_id = 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb') = 0,
    'Alice cannot see Bob tasks'
);

-- Test 3: Alice sees exactly 2 rows total
SELECT ok(
    (SELECT count(*) FROM tasks) = 2,
    'Alice sees exactly 2 rows total (her own)'
);

-- ================================================================
-- Test as Bob
-- ================================================================
SET LOCAL app.end_user_id = 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb';

-- Test 4: Bob can see his own tasks
SELECT ok(
    (SELECT count(*) FROM tasks WHERE user_id = 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb') = 2,
    'Bob can see his 2 tasks'
);

-- Test 5: Bob cannot see Alice's tasks
SELECT ok(
    (SELECT count(*) FROM tasks WHERE user_id = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa') = 0,
    'Bob cannot see Alice tasks'
);

-- Test 6: Bob sees exactly 2 rows total
SELECT ok(
    (SELECT count(*) FROM tasks) = 2,
    'Bob sees exactly 2 rows total (his own)'
);

SELECT * FROM finish();
ROLLBACK;
