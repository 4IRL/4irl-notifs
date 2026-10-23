-- Ownership: give destinations, routes and heartbeat checks a server-side owner,
-- either the operator or exactly one app, so a later phase can let an app manage
-- only its own objects through app-scoped endpoints without trusting the caller.
--
-- 0002 reserved destinations.owner_kind as inert ("widened by phases 8/9"); this
-- widens it to ('operator', 'app') and adds owner_app_id beside it, then adds the
-- same owner_kind / owner_app_id pair to routes and heartbeat_checks. Every
-- existing row stays 'operator' (owner_app_id NULL): no app has app-scoped access
-- yet, so nothing is app-owned until a later step writes it.
--
-- The CHECKs keep an owner's objects inside its own namespace at the database
-- level, whatever the application layer does:
--   - owner_kind = 'app' exactly when owner_app_id is set;
--   - an app-owned destination's name starts with '<owner>_' (an exact substring
--     compare, not LIKE, so an owner_app_id containing '_' cannot widen its own
--     prefix through LIKE's wildcard);
--   - an app-owned route's app_pattern is the owner (never '*');
--   - an app-owned check's app_id is the owner.

ALTER TABLE destinations DROP CONSTRAINT destinations_owner_kind_check;
ALTER TABLE destinations ADD CONSTRAINT destinations_owner_kind_check
  CHECK (owner_kind IN ('operator', 'app'));
ALTER TABLE destinations ADD COLUMN owner_app_id text NULL
  CONSTRAINT destinations_owner_app_id_format_check CHECK (owner_app_id ~ '^[a-z0-9][a-z0-9_]{0,62}$');
ALTER TABLE destinations ADD CONSTRAINT destinations_owner_pair_check
  CHECK ((owner_kind = 'app') = (owner_app_id IS NOT NULL));
ALTER TABLE destinations ADD CONSTRAINT destinations_owner_name_prefix_check
  CHECK (owner_app_id IS NULL OR left(name, length(owner_app_id) + 1) = owner_app_id || '_');

ALTER TABLE routes ADD COLUMN owner_kind text NOT NULL DEFAULT 'operator'
  CONSTRAINT routes_owner_kind_check CHECK (owner_kind IN ('operator', 'app'));
ALTER TABLE routes ADD COLUMN owner_app_id text NULL
  CONSTRAINT routes_owner_app_id_format_check CHECK (owner_app_id ~ '^[a-z0-9][a-z0-9_]{0,62}$');
ALTER TABLE routes ADD CONSTRAINT routes_owner_pair_check
  CHECK ((owner_kind = 'app') = (owner_app_id IS NOT NULL));
ALTER TABLE routes ADD CONSTRAINT routes_owner_app_pattern_check
  CHECK (owner_app_id IS NULL OR app_pattern = owner_app_id);

ALTER TABLE heartbeat_checks ADD COLUMN owner_kind text NOT NULL DEFAULT 'operator'
  CONSTRAINT heartbeat_checks_owner_kind_check CHECK (owner_kind IN ('operator', 'app'));
ALTER TABLE heartbeat_checks ADD COLUMN owner_app_id text NULL
  CONSTRAINT heartbeat_checks_owner_app_id_format_check CHECK (owner_app_id ~ '^[a-z0-9][a-z0-9_]{0,62}$');
ALTER TABLE heartbeat_checks ADD CONSTRAINT heartbeat_checks_owner_pair_check
  CHECK ((owner_kind = 'app') = (owner_app_id IS NOT NULL));
ALTER TABLE heartbeat_checks ADD CONSTRAINT heartbeat_checks_owner_app_id_check
  CHECK (owner_app_id IS NULL OR app_id = owner_app_id);
