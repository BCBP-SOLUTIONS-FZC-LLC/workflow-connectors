-- Send intents (PostgreSQL 13+), applied by sendintent/sqlstore.ApplySchema
-- through platform-pgcommon's migrate.Runner, tracked in
-- connector_send_intents_migrations. Duplicate-request protection for
-- send-email: one intent per tenant + caller-supplied messageKey. This is not
-- idempotency — a provider may still deliver an explicitly resent message twice.
CREATE TABLE IF NOT EXISTS connector_send_intents (
    id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           text        NOT NULL,
    message_key         text        NOT NULL,
    status              text        NOT NULL,
    attempts            integer     NOT NULL DEFAULT 1,
    provider_message_id text        NOT NULL DEFAULT '',
    detail              text        NOT NULL DEFAULT '',
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT uq_connector_send_intents_key UNIQUE (tenant_id, message_key),
    CONSTRAINT chk_connector_send_intents_status
        CHECK (status IN ('pending', 'accepted', 'not_delivered', 'unknown'))
);
