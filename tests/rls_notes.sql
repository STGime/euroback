-- pgTAP tests for RLS policies on the notes table
-- Policy: public notes visible to all, private notes only to owner

BEGIN;

SELECT plan(6);

-- ================================================================
-- Test as Alice
-- ================================================================
SET LOCAL app.end_user_id = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa';
SET LOCAL app.end_user_role = 'authenticated';

-- Test 1: Alice can see all public notes (from anyone)
SELECT ok(
    (SELECT count(*) FROM notes WHERE is_public = true) = 2,
    'Alice can see all 2 public notes'
);

-- Test 2: Alice can see her own private note
SELECT ok(
    EXISTS(SELECT 1 FROM notes WHERE title = 'Alice private note'),
    'Alice can see her private note'
);

-- Test 3: Alice cannot see Bob's private note
SELECT ok(
    NOT EXISTS(SELECT 1 FROM notes WHERE title = 'Bob private note'),
    'Alice cannot see Bob private note'
);

-- ================================================================
-- Test as Bob
-- ================================================================
SET LOCAL app.end_user_id = 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb';

-- Test 4: Bob can see all public notes
SELECT ok(
    (SELECT count(*) FROM notes WHERE is_public = true) = 2,
    'Bob can see all 2 public notes'
);

-- Test 5: Bob can see his own private note
SELECT ok(
    EXISTS(SELECT 1 FROM notes WHERE title = 'Bob private note'),
    'Bob can see his private note'
);

-- Test 6: Bob cannot see Alice's private note
SELECT ok(
    NOT EXISTS(SELECT 1 FROM notes WHERE title = 'Alice private note'),
    'Bob cannot see Alice private note'
);

SELECT * FROM finish();
ROLLBACK;
