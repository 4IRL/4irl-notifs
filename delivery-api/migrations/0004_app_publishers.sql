-- Per-app ntfy publisher tokens, and the no_publisher dead reason.
--
-- app_publishers holds one sealed ntfy publisher token per app. delivery-api
-- mints it through provisioning-api (POST /v1/provision-app) from an explicit
-- operator endpoint and uses it to publish to-recipient envelopes to
-- {app_id}-{person_hash}-{channel}. sealed_token is AES-GCM sealed with the app
-- id as additional data (see internal/publisher); the plaintext token is never
-- stored. publisher_user_id and topic_pattern are the non-secret fields
-- provisioning-api returned alongside it.
--
-- A recipient delivery whose app has no publisher cannot be sent, so the dead
-- reason list gains no_publisher.

CREATE TABLE app_publishers (
  app_id            text PRIMARY KEY CHECK (app_id ~ '^[a-z0-9][a-z0-9_]{0,62}$'),
  publisher_user_id text NOT NULL,
  topic_pattern     text NOT NULL,
  sealed_token      bytea NOT NULL,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE message_deliveries DROP CONSTRAINT message_deliveries_dead_reason_check;
ALTER TABLE message_deliveries ADD CONSTRAINT message_deliveries_dead_reason_check
  CHECK (dead_reason IN ('terminal_status', 'gone', 'budget_exhausted', 'destination_disabled', 'destination_deleted', 'blocked_address', 'unsupported_type', 'no_publisher'));
