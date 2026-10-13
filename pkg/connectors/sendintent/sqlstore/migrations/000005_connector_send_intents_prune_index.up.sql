-- Serves PruneIntents: finished (not pending) intents by age. The migration
-- runner applies each file in a transaction, where CREATE INDEX CONCURRENTLY
-- is impossible, so the index is built with a plain CREATE INDEX: it holds a
-- SHARE lock on connector_send_intents for the build, during which
-- reservations and outcome records wait (reads do not). The table holds one
-- row per messageKey kept for the retention period, so the build is short;
-- on a very large table apply this migration in a quiet period. Idempotent.
CREATE INDEX IF NOT EXISTS ix_connector_send_intents_prune
    ON connector_send_intents (updated_at)
    WHERE status <> 'pending';
