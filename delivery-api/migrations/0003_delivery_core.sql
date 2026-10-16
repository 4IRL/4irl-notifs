-- Delivery core: the immutable message envelope, one queued delivery per
-- resolved destination, and an append-only log of every delivery attempt.
--
-- messages is immutable after insert. All mutable state (pending / delivered /
-- dead, the retry budget, the next attempt time) lives in message_deliveries;
-- the message-level status is derived from its deliveries and never stored.
-- The CHECKs backstop the main identifier and shape rules in internal/message
-- validation (app_id, channel, idempotency_key, to_*, payload and display type)
-- so those bad rows cannot be written even by a code path that skips it; the
-- rest of the validation (sizes, display fields, url, priority) lives only in Go.
--
-- to_recipient is always NULL until phase 4 ships the ntfy sink; the column is
-- reserved now so that phase needs no migration. deliver_at is likewise
-- reserved for phase 6 and is always NULL until then.

CREATE TABLE messages (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  app_id          text NOT NULL CHECK (app_id ~ '^[a-z0-9][a-z0-9_]{0,62}$'),
  channel         text NOT NULL CHECK (channel ~ '^[a-z0-9][a-z0-9_]{0,31}$'),
  idempotency_key text NOT NULL CHECK (idempotency_key ~ '^[\x21-\x7e]{1,200}$'),
  to_destination  text NULL,
  to_recipient    text NULL,
  CHECK (num_nonnulls(to_destination, to_recipient) <= 1),
  deliver_at      timestamptz NULL,
  critical        boolean NOT NULL DEFAULT false,
  payload         jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
  display         jsonb NULL CHECK (display IS NULL OR jsonb_typeof(display) = 'object'),
  created_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (app_id, idempotency_key)
);

-- Used by the retention sweeper, which deletes by age.
CREATE INDEX messages_created_at_idx ON messages (created_at);

CREATE TABLE message_deliveries (
  id                bigserial PRIMARY KEY,
  message_id        uuid NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  -- SET NULL, not RESTRICT: the delivery log must survive an operator deleting
  -- the destination it was sent to. destination_name is the snapshot that keeps
  -- the row meaningful afterwards.
  destination_id    bigint NULL REFERENCES destinations(id) ON DELETE SET NULL,
  destination_name  text NOT NULL,
  destination_type  text NOT NULL CHECK (destination_type IN ('ntfy', 'discord', 'webhook')),
  state             text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'delivered', 'dead')),
  dead_reason       text NULL CHECK (dead_reason IN ('terminal_status', 'gone', 'budget_exhausted', 'destination_disabled', 'destination_deleted', 'blocked_address', 'unsupported_type')),
  CHECK ((state = 'dead') = (dead_reason IS NOT NULL)),
  attempt_count     integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  budget_attempts   integer NOT NULL DEFAULT 0 CHECK (budget_attempts >= 0),
  budget_started_at timestamptz NOT NULL DEFAULT now(),
  next_attempt_at   timestamptz NOT NULL DEFAULT now(),
  last_status_code  integer NULL,
  delivered_at      timestamptz NULL,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  UNIQUE (message_id, destination_name)
);

-- The worker's claim query: due pending rows in order.
CREATE INDEX message_deliveries_due_idx ON message_deliveries (next_attempt_at, id) WHERE state = 'pending';
-- The operator list by state, newest first.
CREATE INDEX message_deliveries_state_updated_idx ON message_deliveries (state, updated_at DESC, id DESC);
-- Keeps the ON DELETE SET NULL cascade from a destination delete off a seq scan.
CREATE INDEX message_deliveries_destination_id_idx ON message_deliveries (destination_id);

CREATE TABLE delivery_attempts (
  id               bigserial PRIMARY KEY,
  delivery_id      bigint NOT NULL REFERENCES message_deliveries(id) ON DELETE CASCADE,
  attempt_number   integer NOT NULL,
  outcome          text NOT NULL CHECK (outcome IN ('success', 'retryable', 'terminal', 'gone', 'blocked')),
  status_code      integer NULL,
  latency_ms       integer NOT NULL CHECK (latency_ms >= 0),
  error            text NULL CHECK (char_length(error) <= 500),
  response_snippet text NULL CHECK (octet_length(response_snippet) <= 1024),
  started_at       timestamptz NOT NULL,
  UNIQUE (delivery_id, attempt_number)
);
