-- Supports PruneAttempts: audit rows are deleted by age, oldest first.
CREATE INDEX IF NOT EXISTS idx_connector_document_attempts_finished_at
    ON connector_document_attempts (finished_at);
