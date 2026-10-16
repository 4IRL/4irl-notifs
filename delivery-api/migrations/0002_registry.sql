-- Registry: operator-managed destinations and the routes that fan an envelope
-- out to them.
--
-- owner_kind is widened by phases 8/9 (app / person owners); until then the only
-- allowed value is 'operator'. enabled / disabled_reason are what phase 3's
-- 410 handling writes when a sink reports a destination permanently gone.

CREATE TABLE destinations (
  id              bigserial PRIMARY KEY,
  name            text NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9][a-z0-9_]{0,62}$'),
  type            text NOT NULL CHECK (type IN ('ntfy', 'discord', 'webhook')),
  owner_kind      text NOT NULL DEFAULT 'operator' CHECK (owner_kind IN ('operator')),
  config          jsonb NOT NULL DEFAULT '{}'::jsonb,
  sealed_secrets  bytea NOT NULL,
  enabled         boolean NOT NULL DEFAULT true,
  disabled_reason text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE routes (
  id              bigserial PRIMARY KEY,
  app_pattern     text NOT NULL CHECK (app_pattern = '*' OR app_pattern ~ '^[a-z0-9][a-z0-9_]{0,62}$'),
  channel_pattern text NOT NULL CHECK (channel_pattern = '*' OR channel_pattern ~ '^[a-z0-9][a-z0-9_]{0,31}$' OR channel_pattern ~ '^[a-z0-9][a-z0-9_]{0,30}\*$'),
  enabled         boolean NOT NULL DEFAULT true,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (app_pattern, channel_pattern)
);

CREATE TABLE route_destinations (
  route_id       bigint NOT NULL REFERENCES routes(id) ON DELETE CASCADE,
  destination_id bigint NOT NULL REFERENCES destinations(id) ON DELETE RESTRICT,
  PRIMARY KEY (route_id, destination_id)
);

CREATE INDEX route_destinations_destination_id_idx ON route_destinations (destination_id);
