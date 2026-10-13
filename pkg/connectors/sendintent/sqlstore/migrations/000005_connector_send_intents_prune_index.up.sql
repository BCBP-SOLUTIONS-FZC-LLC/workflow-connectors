-- Serves PruneIntents: finished (not pending) intents by age. The migration
-- runner applies each file in a transaction, where CREATE INDEX CONCURRENTLY
-- is impossible, so the index is built with a plain CREATE INDEX: it holds a
-- SHARE lock on connector_send_intents for the build, during which
-- reservations and outcome records wait (reads do not). The table holds one
-- row per messageKey kept for the retention period, so the build is short;
-- on a very large table an operator may build it beforehand with
-- CREATE INDEX CONCURRENTLY (same name and definition) in a quiet period.
--
-- Idempotent, and repairs a failed concurrent build: CREATE INDEX IF NOT
-- EXISTS would keep an INVALID index (left by a CREATE INDEX CONCURRENTLY
-- that failed or was cancelled), which the planner never uses and every
-- write still maintains. Such an index is dropped and rebuilt; a valid one is
-- kept as is.
DO $$
DECLARE
    existing record;
BEGIN
    SELECT n.nspname AS schema_name, c.relname AS index_name, i.indisvalid AS valid
      INTO existing
      FROM pg_index i
      JOIN pg_class c ON c.oid = i.indexrelid
      JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE i.indrelid = 'connector_send_intents'::regclass
       AND c.relname = 'ix_connector_send_intents_prune';
    IF FOUND AND existing.valid THEN
        RETURN;
    END IF;
    IF FOUND THEN
        EXECUTE format('DROP INDEX %I.%I', existing.schema_name, existing.index_name);
    END IF;
    CREATE INDEX ix_connector_send_intents_prune
        ON connector_send_intents (updated_at)
        WHERE status <> 'pending';
END
$$;
