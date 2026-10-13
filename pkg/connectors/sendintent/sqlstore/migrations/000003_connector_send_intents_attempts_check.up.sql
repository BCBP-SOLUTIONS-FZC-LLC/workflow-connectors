-- attempts counts reservations: 1 when the intent is created, +1 on each
-- re-reservation, so it is never below 1 (the explicit-resend
-- compare-and-swap relies on it). The constraint is added NOT VALID: the
-- ALTER takes its ACCESS EXCLUSIVE lock only for the catalog change, without
-- scanning the table, and new and updated rows are checked from then on.
-- Existing rows are validated by 000004 in a migration of its own: the
-- runner applies each file as one implicit transaction, so a VALIDATE here
-- would hold this ALTER's lock for the whole scan. Idempotent: the
-- constraint is added only when missing.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'chk_connector_send_intents_attempts'
          AND conrelid = 'connector_send_intents'::regclass
    ) THEN
        ALTER TABLE connector_send_intents
            ADD CONSTRAINT chk_connector_send_intents_attempts CHECK (attempts >= 1) NOT VALID;
    END IF;
END
$$;
