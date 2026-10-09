-- Connector document registry (PostgreSQL 13+), applied by sqlstore.ApplySchema
-- through platform-pgcommon's migrate.Runner, tracked in its own table
-- (connector_documents_migrations) so its versions never collide with the
-- service's. Uniqueness of a logical document is enforced here, by
-- uq_connector_documents_identity — never by the object store.
CREATE TABLE IF NOT EXISTS connector_documents (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        text        NOT NULL,
    provider         text        NOT NULL,
    container        text        NOT NULL,
    filename         text        NOT NULL,
    state            text        NOT NULL,
    version          bigint      NOT NULL DEFAULT 1,
    object_id        text        NOT NULL DEFAULT '',
    content_type     text        NOT NULL DEFAULT '',
    size_bytes       bigint      NOT NULL DEFAULT 0,
    owner            text,
    lease_expires_at timestamptz,
    claimed_at       timestamptz,
    last_error       text        NOT NULL DEFAULT '',
    failed_attempts  integer     NOT NULL DEFAULT 0,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT uq_connector_documents_identity UNIQUE (tenant_id, provider, container, filename),
    CONSTRAINT chk_connector_documents_state
        CHECK (state IN ('PENDING_UPLOAD', 'UPLOADING', 'AVAILABLE', 'FAILED', 'DELETING')),
    -- A busy row always has an owner and a lease; an idle row never does.
    CONSTRAINT chk_connector_documents_owner
        CHECK ((state IN ('AVAILABLE', 'FAILED')) = (owner IS NULL AND lease_expires_at IS NULL))
);

-- One row per finished attempt. No foreign key: a deleted document keeps its
-- audit trail.
CREATE TABLE IF NOT EXISTS connector_document_attempts (
    id          bigserial   PRIMARY KEY,
    document_id uuid        NOT NULL,
    tenant_id   text        NOT NULL,
    attempt     text        NOT NULL,
    outcome     text        NOT NULL,
    error       text        NOT NULL DEFAULT '',
    started_at  timestamptz,
    finished_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT chk_connector_document_attempts_outcome
        CHECK (outcome IN ('available', 'failed', 'deleted'))
);

CREATE INDEX IF NOT EXISTS idx_connector_document_attempts_document
    ON connector_document_attempts (document_id);
