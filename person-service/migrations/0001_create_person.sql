-- person is a reverse-index mapping a stable, non-reversible person_hash back
-- to the email address it was derived from. Consuming services only ever
-- learn a person_hash (never a raw email); this table is the sole place that
-- can resolve one back to the other, and it exists so notification delivery
-- can look up "which email does this person_hash belong to" without every
-- caller needing to store emails itself.
CREATE TABLE person (
  person_hash TEXT PRIMARY KEY,
  email TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE INDEX idx_person_email ON person(email);
