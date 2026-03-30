-- pgTAP tests for anonymous (unauthenticated) access
-- Anonymous users should only see public notes, no tasks

BEGIN;

SELECT plan(3);

-- ================================================================
-- Test as anonymous (no end_user_id set, role = anon)
-- ================================================================
SET LOCAL app.end_user_id = '';
SET LOCAL app.end_user_role = 'anon';

-- Test 1: Anonymous cannot see any tasks (owner_access policy)
SELECT ok(
    (SELECT count(*) FROM tasks) = 0,
    'Anonymous cannot see any tasks'
);

-- Test 2: Anonymous can see public notes
SELECT ok(
    (SELECT count(*) FROM notes WHERE is_public = true) = 2,
    'Anonymous can see public notes'
);

-- Test 3: Anonymous cannot see private notes
SELECT ok(
    (SELECT count(*) FROM notes WHERE is_public = false) = 0,
    'Anonymous cannot see private notes'
);

SELECT * FROM finish();
ROLLBACK;
