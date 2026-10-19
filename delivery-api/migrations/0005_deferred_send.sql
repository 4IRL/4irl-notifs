-- Deferred send: hold a message until its deliver_at, release it into the queue
-- when due, and let it be cancelled or replaced until then.
--
-- This supersedes 0003's note that deliver_at is "reserved and always NULL": it
-- is now set on a message submitted with a future (or explicit) deliver_at.
--
-- A scheduled message's deliveries are inserted with state 'scheduled' and
-- next_attempt_at = deliver_at. A release poller flips them to 'pending', which
-- is the release into the queue; the worker's claim query only ever claims
-- 'pending', so a scheduled row can never be claimed. Cancelling a scheduled
-- message moves its deliveries to 'cancelled' and stamps messages.cancelled_at.
-- The message-level status stays derived from the delivery states, never stored.
--
-- "Scheduled" lives on message_deliveries.state, so the per-app cap count, the
-- operator list and the release poller all probe the partial index below; app_id
-- is deliberately not denormalized onto message_deliveries.

ALTER TABLE message_deliveries DROP CONSTRAINT message_deliveries_state_check;
ALTER TABLE message_deliveries ADD CONSTRAINT message_deliveries_state_check
  CHECK (state IN ('pending', 'delivered', 'dead', 'scheduled', 'cancelled'));

ALTER TABLE messages ADD COLUMN cancelled_at timestamptz NULL;
ALTER TABLE messages ADD CONSTRAINT messages_cancelled_at_check
  CHECK (cancelled_at IS NULL OR deliver_at IS NOT NULL);

-- The release poller's due probe, the per-app cap count and the admin list.
CREATE INDEX message_deliveries_scheduled_idx ON message_deliveries (next_attempt_at, message_id) WHERE state = 'scheduled';
