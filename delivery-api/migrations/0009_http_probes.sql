-- HTTP probes: a heartbeat check can be a probe check, one the service itself
-- drives by requesting a URL on a schedule instead of waiting for a push ping.
--
-- Kind is row presence: a check is a probe check exactly when it has a
-- heartbeat_check_probes row (1:1, deleted with its check). There is no kind
-- column, so the database cannot hold a probe row without a probe kind or the
-- reverse. Push checks have no row and are untouched.
--
-- heartbeat_checks.down_reason is widened from ('missed','fail') to also hold the
-- eight probe failure reasons. The Go reason constants widen together with this
-- list. The auto-named inline CHECK is dropped and re-added (precedent: 0004,
-- 0007). Forward-only: there is no down migration.
--
-- The side table holds the probe's settings (url, expected statuses, optional body
-- match, timeout, interval), its schedule (next_probe_at, config_version,
-- consecutive_failures), its latest result and the sealed Access credentials. The
-- only secret column is sealed_access_headers (bytea); the sealed_ prefix is the
-- repo convention for secret columns. The numeric, length and set-size bounds are
-- enforced here; host rules, ordering and content rules stay in Go.

ALTER TABLE heartbeat_checks DROP CONSTRAINT heartbeat_checks_down_reason_check;
ALTER TABLE heartbeat_checks ADD CONSTRAINT heartbeat_checks_down_reason_check
  CHECK (down_reason IN ('missed', 'fail', 'http_status', 'timeout', 'body_mismatch', 'blocked', 'tls', 'scheme', 'network', 'config'));

CREATE TABLE heartbeat_check_probes (
  check_id              bigint PRIMARY KEY REFERENCES heartbeat_checks(id) ON DELETE CASCADE,
  url                   text NOT NULL CHECK (url ~ '^https://' AND octet_length(url) <= 2048),
  expected_statuses     integer[] NOT NULL CHECK (
    cardinality(expected_statuses) BETWEEN 1 AND 10
    AND 100 <= ALL(expected_statuses)
    AND 599 >= ALL(expected_statuses)
    AND array_position(expected_statuses, NULL) IS NULL),
  body_match            text NULL CHECK (body_match IS NULL OR octet_length(body_match) BETWEEN 1 AND 256),
  timeout_seconds       integer NOT NULL CHECK (timeout_seconds BETWEEN 1 AND 8),
  interval_seconds      integer NOT NULL CHECK (interval_seconds BETWEEN 60 AND 86400),
  consecutive_failures  integer NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
  next_probe_at         timestamptz NOT NULL,
  config_version        bigint NOT NULL DEFAULT 1 CHECK (config_version >= 1),
  last_result_at        timestamptz NULL,
  last_result_status    integer NULL CHECK (last_result_status BETWEEN 100 AND 599),
  last_result_reason    text NULL CHECK (last_result_reason IN ('http_status', 'timeout', 'body_mismatch', 'blocked', 'tls', 'scheme', 'network', 'config')),
  last_result_detail    text NULL CHECK (last_result_detail IS NULL OR octet_length(last_result_detail) <= 256),
  down_detail           text NULL CHECK (down_detail IS NULL OR octet_length(down_detail) <= 256),
  sealed_access_headers bytea NULL,
  -- Defence in depth: the two range CHECKs already imply it (timeout max 8 < interval min 60).
  CONSTRAINT heartbeat_check_probes_timeout_below_interval_check
    CHECK (timeout_seconds < interval_seconds),
  CONSTRAINT heartbeat_check_probes_last_result_check
    CHECK (last_result_at IS NOT NULL OR (last_result_status IS NULL AND last_result_reason IS NULL AND last_result_detail IS NULL))
);

CREATE INDEX heartbeat_check_probes_next_probe_at_idx ON heartbeat_check_probes (next_probe_at);
