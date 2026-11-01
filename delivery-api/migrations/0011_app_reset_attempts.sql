-- Per-app, per-person reset-password attempts, the durable store behind the app-route reset door limit
-- (resetlimit.AppLimit per app and resetlimit.PersonLimit per app+person, each per resetlimit.Window).
-- One row per attempt that was admitted to the upstream relay; person_hash is a one-way hash, never the
-- email. No FK to apps: rows older than the window are deleted by the per-reserve prune and by the
-- retention sweeper (appidentity.RunReplayCleanup). Forward-only: there is no down migration.

CREATE TABLE app_reset_attempts (
  app_id       text        NOT NULL,
  person_hash  text        NOT NULL,
  attempted_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX app_reset_attempts_app_attempted_idx ON app_reset_attempts (app_id, attempted_at);
CREATE INDEX app_reset_attempts_person_attempted_idx ON app_reset_attempts (app_id, person_hash, attempted_at);
