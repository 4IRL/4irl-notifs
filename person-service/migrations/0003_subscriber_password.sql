-- Subscriber-generated passwords: provisioning-api now creates each person's
-- ntfy password itself and must remember, across redeploys of its single
-- container, (a) whether a person already has one and (b) how often it has
-- been reset recently.
--
-- password_set_at is a nullable ISO-8601 UTC stamp (like created_at). NULL
-- means "no known password"; there is deliberately no backfill, so every
-- person that predates this migration starts NULL. It is never returned by
-- GET /people or GET /person.
ALTER TABLE person ADD COLUMN password_set_at TEXT;

-- password_reset is the rolling-window history behind the per-person reset
-- limit (5 per hour). reset_at is epoch milliseconds.
CREATE TABLE password_reset (
  reset_id TEXT PRIMARY KEY,
  person_hash TEXT NOT NULL,
  reset_at INTEGER NOT NULL
);

CREATE INDEX idx_password_reset_person_hash_reset_at ON password_reset (person_hash, reset_at);
