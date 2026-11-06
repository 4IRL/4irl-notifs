-- Held messages: an app message with no matching route is accepted and held on
-- its messages row (no delivery rows) until a route exists, then released.
--
-- The state lives on messages: held_at (held, not yet released or cancelled),
-- held_cancelled_at (witness that a held message was cancelled; cancelled_at is
-- set to the same instant), released_at (the hold was released into deliveries),
-- release_failed_at + release_failed_reason (a release that could not build
-- deliveries; reason is currently only 'release_invalid'). The message-level
-- status stays derived, never stored.
--
-- This is NOT purely additive: it replaces messages_cancelled_at_check (0005:
-- cancelled_at IS NULL OR deliver_at IS NOT NULL) so a held message, which has
-- no deliver_at, can be cancelled. messages_held_cancelled_check keeps the
-- witness from being set without the cancel itself. message_deliveries'
-- dead_reason CHECK is not touched.
--
-- This also supersedes 0003's note that messages are immutable: a message row
-- also mutates on cancel, on scheduled replace and, from here, on the held
-- columns.

ALTER TABLE messages ADD COLUMN held_at timestamptz NULL;
ALTER TABLE messages ADD COLUMN held_cancelled_at timestamptz NULL;
ALTER TABLE messages ADD COLUMN released_at timestamptz NULL;
ALTER TABLE messages ADD COLUMN release_failed_at timestamptz NULL;
ALTER TABLE messages ADD COLUMN release_failed_reason text NULL;

ALTER TABLE messages ADD CONSTRAINT messages_release_failed_pair_check
  CHECK ((release_failed_at IS NULL) = (release_failed_reason IS NULL));
ALTER TABLE messages ADD CONSTRAINT messages_release_failed_reason_check
  CHECK (release_failed_reason IS NULL OR release_failed_reason IN ('release_invalid'));
ALTER TABLE messages ADD CONSTRAINT messages_held_cancelled_check
  CHECK (held_cancelled_at IS NULL OR cancelled_at IS NOT NULL);

ALTER TABLE messages DROP CONSTRAINT messages_cancelled_at_check;
ALTER TABLE messages ADD CONSTRAINT messages_cancelled_at_check
  CHECK (cancelled_at IS NULL OR deliver_at IS NOT NULL OR held_cancelled_at IS NOT NULL);

-- The release sweep's oldest-first probe and the per-app held cap count.
CREATE INDEX messages_held_idx ON messages (held_at, id) WHERE held_at IS NOT NULL;
CREATE INDEX messages_held_app_idx ON messages (app_id) WHERE held_at IS NOT NULL;
