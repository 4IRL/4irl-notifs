-- Per-app publisher mint attempts, the durable store behind the app-route mint rate limit
-- (publisher.MintRateLimit per publisher.MintRateWindow). One row per attempt that reached the
-- upstream minter. No FK to apps: rows older than the window are deleted by the per-mint prune and
-- by the retention sweeper (appidentity.RunReplayCleanup). Forward-only: there is no down migration.

CREATE TABLE app_publisher_mints (
  app_id    text        NOT NULL,
  minted_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX app_publisher_mints_app_minted_idx ON app_publisher_mints (app_id, minted_at);
