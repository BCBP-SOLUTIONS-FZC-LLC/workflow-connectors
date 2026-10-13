-- Validates chk_connector_send_intents_attempts (added NOT VALID by 000003)
-- against the existing rows. VALIDATE CONSTRAINT takes only a SHARE UPDATE
-- EXCLUSIVE lock for its scan: reservations, records and pruning carry on
-- (only other schema changes wait). A no-op once the constraint is valid.
-- Rows written by this library always satisfy it; a row edited by hand to
-- attempts < 1 makes this migration fail until it is corrected.
ALTER TABLE connector_send_intents VALIDATE CONSTRAINT chk_connector_send_intents_attempts;
