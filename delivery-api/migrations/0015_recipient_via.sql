-- Per-message delivery choice for a to.recipient message: which legs it takes
-- (to_recipient_via: ntfy, personal or both) and which of the person's personal
-- destinations receive it (to_personal_destination_ids, stored sorted ascending).
-- Both are persisted so a retry of a still-scheduled message that differs only in
-- routing is a replace, not a silent duplicate.
--
-- Forward-only, no backfill and no default (metadata-only, the table is not
-- rewritten): existing rows stay NULL and read back as ntfy with no ids.
--
--   to_recipient_via             NULL = unset (ntfy); never an empty string
--   to_personal_destination_ids  NULL unless via is personal or both; never an
--                                empty array
--
-- Both are NULL on a message without a to.recipient.

ALTER TABLE messages
  ADD COLUMN to_recipient_via text NULL
    CONSTRAINT messages_to_recipient_via_check CHECK (to_recipient_via IN ('ntfy', 'personal', 'both')),
  ADD COLUMN to_personal_destination_ids bigint[] NULL
    CONSTRAINT messages_to_personal_destination_ids_nonempty_check
      CHECK (to_personal_destination_ids IS NULL OR cardinality(to_personal_destination_ids) > 0),
  ADD CONSTRAINT messages_recipient_via_needs_recipient_check
    CHECK (to_recipient IS NOT NULL OR (to_recipient_via IS NULL AND to_personal_destination_ids IS NULL)),
  ADD CONSTRAINT messages_ids_match_via_check
    CHECK ((to_personal_destination_ids IS NULL) = (to_recipient_via IS NULL OR to_recipient_via = 'ntfy'));
