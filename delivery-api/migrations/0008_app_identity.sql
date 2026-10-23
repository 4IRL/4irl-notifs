-- App identity: delivery-api's own per-app identity, independent of the Cloudflare Access
-- token. An app's deploy proves its GitHub Actions identity with an OIDC token; the first
-- exchange files a pending registration request, the operator approves it into a binding
-- (app_id <- GitHub repository + workflow + ref + environment), and every later exchange
-- from that same workflow identity is issued a fresh app key (stored only as a SHA-256 hash).
--
-- Four tables, no data: nothing is bound, requested, issued or replayed until a later step
-- adds the endpoints that write them.
--
--   app_bindings              the approved identity of an app (one per app_id; one workflow
--                             identity can be bound to only one app)
--   app_registration_requests the operator's approval queue (pending, then rejected)
--   app_keys                  issued keys: hash and display hint only, never the plaintext
--   oidc_replay               OIDC token ids already exchanged, swept after the token expires
--
-- environment is NULL when the workflow ran with no GitHub environment. Both unique
-- constraints below are NULLS NOT DISTINCT, so "no environment" is one value, not "every
-- NULL is different": two bindings (or two pending requests) with the same claims and a NULL
-- environment collide, as they must.

CREATE TABLE app_bindings (
  app_id               text PRIMARY KEY CHECK (app_id ~ '^[a-z0-9][a-z0-9_]{0,62}$'),
  github_repository    text NOT NULL CHECK (github_repository <> ''), -- owner/name, display only
  github_repository_id bigint NOT NULL CHECK (github_repository_id > 0),
  github_owner_id      bigint NOT NULL CHECK (github_owner_id > 0),
  workflow_path        text NOT NULL CHECK (workflow_path <> ''),
  git_ref              text NOT NULL CHECK (git_ref <> ''),
  environment          text NULL CHECK (environment IS NULL OR environment <> ''),
  approved_at          timestamptz NOT NULL,
  created_at           timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT app_bindings_workflow_identity_key
    UNIQUE NULLS NOT DISTINCT (github_repository_id, workflow_path, git_ref, environment)
);

CREATE TABLE app_registration_requests (
  id                   bigserial PRIMARY KEY,
  app_id               text NOT NULL CHECK (app_id ~ '^[a-z0-9][a-z0-9_]{0,62}$'),
  github_repository    text NOT NULL CHECK (github_repository <> ''),
  github_repository_id bigint NOT NULL CHECK (github_repository_id > 0),
  github_owner_id      bigint NOT NULL CHECK (github_owner_id > 0),
  workflow_path        text NOT NULL CHECK (workflow_path <> ''),
  git_ref              text NOT NULL CHECK (git_ref <> ''),
  environment          text NULL CHECK (environment IS NULL OR environment <> ''),
  github_run_id        text NOT NULL CHECK (github_run_id <> ''),
  status               text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'rejected')),
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  decided_at           timestamptz NULL,
  -- A rejected request records when it was rejected; a pending one has not been decided.
  CONSTRAINT app_registration_requests_decided_check CHECK ((status = 'rejected') = (decided_at IS NOT NULL))
);

-- Re-exchanging with the same claims refreshes the one pending row instead of adding another.
CREATE UNIQUE INDEX app_registration_requests_pending_key
  ON app_registration_requests (app_id, github_repository_id, workflow_path, git_ref, environment)
  NULLS NOT DISTINCT
  WHERE status = 'pending';

-- The operator's list: pending first, then recent rejected, newest first within each.
CREATE INDEX app_registration_requests_status_idx ON app_registration_requests (status, updated_at DESC);

CREATE TABLE app_keys (
  id             bigserial PRIMARY KEY,
  app_id         text NOT NULL REFERENCES app_bindings (app_id) ON DELETE CASCADE,
  -- SHA-256 of the full key string. The plaintext is shown once, at issue, and never stored.
  key_hash       bytea NOT NULL UNIQUE CHECK (octet_length(key_hash) = 32),
  key_hint       text NOT NULL CHECK (key_hint <> ''), -- 'dak_' + the first 4 random characters, display only
  created_at     timestamptz NOT NULL DEFAULT now(),
  superseded_at  timestamptz NULL, -- set when a newer key is issued; the key works for 24 h more
  revoked_at     timestamptz NULL,
  revoked_reason text NULL CHECK (revoked_reason IN ('operator', 'unbound')),
  last_used_at   timestamptz NULL,
  github_run_id  text NULL,
  CONSTRAINT app_keys_revoked_pair_check CHECK ((revoked_at IS NULL) = (revoked_reason IS NULL))
);

CREATE INDEX app_keys_app_id_idx ON app_keys (app_id);

-- Replay protection for the OIDC token exchange: a jti is recorded in the exchange
-- transaction, so a second exchange of the same token fails. Rows are swept once expires_at
-- (the token's expiry plus its clock leeway) has passed.
CREATE TABLE oidc_replay (
  jti        text PRIMARY KEY CHECK (jti <> ''),
  expires_at timestamptz NOT NULL
);

CREATE INDEX oidc_replay_expires_at_idx ON oidc_replay (expires_at);
