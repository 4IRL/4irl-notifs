-- App identity registrars: one approved workflow identity plus one reserved app-id prefix
-- (e.g. 'hosted_') that may create and key many apps under that prefix, so a monorepo that
-- deploys many apps from one workflow (the apps platform) is not limited to one app_id.
--
-- Forward-only reshaping, no backfill and no compatibility path (no apps are registered when
-- this lands):
--
--   app_registrars            new: the registrar (workflow identity + prefix)
--   app_bindings              gains registrar_id (set for registrar-created apps, which copy the
--                             registrar's workflow identity) and acknowledged_at (NULL = the
--                             operator has not yet seen an auto-created app). The workflow
--                             identity stays unique only among normal bindings (registrar_id
--                             IS NULL); the schema does not stop a normal and a registrar binding
--                             sharing an identity, the store enforces that exclusion.
--   app_registration_requests gains kind ('app' | 'registrar'); a registrar request has no app_id
--
-- The prefix CHECK mirrors the application grammar: 2-32 chars ending in '_', never starting
-- with 'platform_' (the shared-destination namespace) and never the stem 'everyone'.
-- environment is NULL when the workflow ran with no GitHub environment (NULLS NOT DISTINCT).

CREATE TABLE app_registrars (
  id                   bigserial PRIMARY KEY,
  prefix               text NOT NULL UNIQUE
                         CHECK (prefix ~ '^[a-z0-9][a-z0-9_]{0,30}_$' AND prefix NOT LIKE 'platform\_%' AND prefix <> 'everyone_'),
  github_repository    text NOT NULL CHECK (github_repository <> ''),
  github_repository_id bigint NOT NULL CHECK (github_repository_id > 0),
  github_owner_id      bigint NOT NULL CHECK (github_owner_id > 0),
  workflow_path        text NOT NULL CHECK (workflow_path <> ''),
  git_ref              text NOT NULL CHECK (git_ref <> ''),
  environment          text NULL CHECK (environment IS NULL OR environment <> ''),
  approved_at          timestamptz NOT NULL,
  created_at           timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT app_registrars_workflow_identity_key
    UNIQUE NULLS NOT DISTINCT (github_repository_id, workflow_path, git_ref, environment)
);

-- Three statements, in this order, so a non-empty table (dev/CI volumes) migrates.
ALTER TABLE app_bindings
  ADD COLUMN registrar_id bigint NULL REFERENCES app_registrars(id) ON DELETE CASCADE,
  ADD COLUMN acknowledged_at timestamptz NULL,
  DROP CONSTRAINT app_bindings_workflow_identity_key;
UPDATE app_bindings SET acknowledged_at = approved_at; -- every pre-existing binding is operator-approved
ALTER TABLE app_bindings
  ADD CONSTRAINT app_bindings_acknowledged_check CHECK (registrar_id IS NOT NULL OR acknowledged_at IS NOT NULL);
CREATE UNIQUE INDEX app_bindings_workflow_identity_key ON app_bindings
  (github_repository_id, workflow_path, git_ref, environment) NULLS NOT DISTINCT WHERE registrar_id IS NULL;
CREATE INDEX app_bindings_registrar_id_idx ON app_bindings (registrar_id) WHERE registrar_id IS NOT NULL;

ALTER TABLE app_registration_requests
  -- The inline column CHECK is named explicitly: left anonymous, Postgres names it
  -- app_registration_requests_kind_check, which collides with the pairing constraint below.
  ADD COLUMN kind text NOT NULL DEFAULT 'app'
    CONSTRAINT app_registration_requests_kind_values_check CHECK (kind IN ('app', 'registrar')),
  ALTER COLUMN app_id DROP NOT NULL,
  ADD CONSTRAINT app_registration_requests_kind_check CHECK ((kind = 'app') = (app_id IS NOT NULL));
-- The pending unique index (app_id, repo id, workflow_path, git_ref, environment) NULLS NOT
-- DISTINCT already gives one pending registrar request per workflow identity (app_id NULL).
