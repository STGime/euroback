-- Recovery migration is idempotent-only; there is no clean rollback
-- because we don't know whether the state we're rolling back was
-- created by 087/088/089 (unlikely — that's the bug we're fixing)
-- or by 094 itself. Refusing to touch anything is the safe posture.
--
-- If a rollback is genuinely needed, execute 000087/088/089's
-- down-migrations manually — they know their own object set — then
-- `migrate force` past 094.

SELECT 1;
