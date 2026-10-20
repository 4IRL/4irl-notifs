-- Heartbeats: apps ping a named check; a missed deadline (anchor_at + period + grace) goes down.
-- "late" is derived at read time, never stored. alert_seq / pending_alert are an outbox written
-- in the same transaction as the state change, so a crash cannot lose or double an alert.

CREATE TABLE heartbeat_checks (
  id               bigserial PRIMARY KEY,
  app_id           text NOT NULL CHECK (app_id ~ '^[a-z0-9][a-z0-9_]{0,62}$'),
  name             text NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9_]{0,62}$'),
  period_seconds   integer NOT NULL CHECK (period_seconds BETWEEN 60 AND 2678400),
  grace_seconds    integer NOT NULL CHECK (grace_seconds BETWEEN 60 AND 604800),
  state            text NOT NULL DEFAULT 'new' CHECK (state IN ('new', 'up', 'down')),
  paused           boolean NOT NULL DEFAULT false,
  anchor_at        timestamptz NOT NULL,
  last_ping_at     timestamptz NULL,
  last_ping_kind   text NULL CHECK (last_ping_kind IN ('success', 'fail')),
  down_since       timestamptz NULL,
  down_reason      text NULL CHECK (down_reason IN ('missed', 'fail')),
  alert_seq        integer NOT NULL DEFAULT 0,
  pending_alert    text NULL CHECK (pending_alert IN ('down', 'recovered')),
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (app_id, name),
  CHECK ((last_ping_at IS NULL) = (last_ping_kind IS NULL)),
  CHECK (state <> 'down' OR (down_since IS NOT NULL AND down_reason IS NOT NULL))
);

-- The checker's due scan: only checks that can still go down (not already down,
-- not paused), oldest anchor first. The deadline is anchor_at plus a per-row
-- offset, so a plain anchor_at index narrows the scan to the live checks.
CREATE INDEX heartbeat_checks_tick_idx ON heartbeat_checks (anchor_at) WHERE state <> 'down' AND NOT paused;

-- The checker's outbox flush.
CREATE INDEX heartbeat_checks_pending_idx ON heartbeat_checks (id) WHERE pending_alert IS NOT NULL;

CREATE TABLE heartbeat_check_destinations (
  check_id       bigint NOT NULL REFERENCES heartbeat_checks(id) ON DELETE CASCADE,
  destination_id bigint NOT NULL REFERENCES destinations(id) ON DELETE RESTRICT,
  PRIMARY KEY (check_id, destination_id)
);

CREATE INDEX heartbeat_check_destinations_destination_id_idx ON heartbeat_check_destinations (destination_id);
