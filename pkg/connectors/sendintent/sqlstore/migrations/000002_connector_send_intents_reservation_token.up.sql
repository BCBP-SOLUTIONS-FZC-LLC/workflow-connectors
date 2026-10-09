-- The caller-generated token of the reservation that made the intent's
-- current attempt. A caller whose Reserve reply was lost (the statement
-- committed, the reply did not arrive) reads the row back and owns the
-- reservation only when the token is its own.
ALTER TABLE connector_send_intents
    ADD COLUMN IF NOT EXISTS reservation_token text NOT NULL DEFAULT '';
