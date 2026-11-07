-- Personal destinations: an end-user's own Discord or webhook endpoint, registered
-- by an app on the person's behalf and opted in per app. The service stores only
-- person_hash (never the email). A to.recipient message fans out to the person's
-- ntfy topic plus every enabled personal destination opted in for the sending app.
--
-- Separate tables, not a third destinations.owner_kind, so the 0007 owner CHECKs,
-- the global UNIQUE(name) and every operator list/route stay untouched.
--
-- No FK on app_id (precedent 0010, 0011): app teardown cleans up explicitly.
-- registrar_removed_at is set when the registering app is torn down; such a row
-- stays while other apps are opted in and is reaped with its last opt-in.
--
-- Also recreates message_deliveries_dead_reason_check with the existing eight
-- values plus 'opted_out' (an opt-in withdrawn after submit dead-letters at send).
-- The personal type is deliberately only discord or webhook, never ntfy.
--
-- Note on earlier migrations: 0003 describes messages.to_recipient as always
-- NULL and deliver_at as reserved; that is historical. to_recipient now holds
-- the person hash of a to.recipient message, and deliver_at is used by
-- scheduled delivery (see 0005).

CREATE TABLE personal_destinations (
  id                    bigserial PRIMARY KEY,
  person_hash           text NOT NULL CHECK (person_hash ~ '^[a-z2-7]{16}$'),
  registered_by_app_id  text NOT NULL CHECK (registered_by_app_id ~ '^[a-z0-9][a-z0-9_]{0,62}$'),
  registrar_removed_at  timestamptz NULL,
  label                 text NOT NULL CHECK (char_length(label) BETWEEN 1 AND 64),
  type                  text NOT NULL CHECK (type IN ('discord', 'webhook')),
  config                jsonb NOT NULL DEFAULT '{}'::jsonb,
  sealed_secrets        bytea NOT NULL,
  enabled               boolean NOT NULL DEFAULT true,
  disabled_by           text NULL CHECK (disabled_by IN ('app', 'operator', 'system')),
  disabled_reason       text NULL,
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT personal_destinations_disabled_pair_check CHECK (enabled = (disabled_by IS NULL))
);
CREATE INDEX personal_destinations_person_idx ON personal_destinations (person_hash);
CREATE INDEX personal_destinations_registrar_idx ON personal_destinations (registered_by_app_id);

CREATE TABLE personal_destination_opt_ins (
  app_id                  text NOT NULL CHECK (app_id ~ '^[a-z0-9][a-z0-9_]{0,62}$'),
  personal_destination_id bigint NOT NULL REFERENCES personal_destinations (id) ON DELETE CASCADE,
  created_at              timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (app_id, personal_destination_id)
);
CREATE INDEX personal_destination_opt_ins_destination_idx ON personal_destination_opt_ins (personal_destination_id);

ALTER TABLE message_deliveries DROP CONSTRAINT message_deliveries_dead_reason_check;
ALTER TABLE message_deliveries ADD CONSTRAINT message_deliveries_dead_reason_check
  CHECK (dead_reason IN ('terminal_status', 'gone', 'budget_exhausted', 'destination_disabled', 'destination_deleted', 'blocked_address', 'unsupported_type', 'no_publisher', 'opted_out'));
