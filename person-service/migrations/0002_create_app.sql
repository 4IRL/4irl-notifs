-- app is the first-class registry of consuming applications. Before this
-- table an "app" was implicit: just an app_id string baked into ntfy topic
-- patterns and token labels, reverse-derived by parsing users' grants. This
-- table gives an app a canonical home plus operator-facing metadata
-- (display_name, description) so the admin UI can list, add, edit, and remove
-- apps as real entities. ntfy remains the source of truth for publisher
-- identities, subscriber grants, and tokens; this registry is advisory
-- metadata layered over that still-derived reality.
CREATE TABLE app (
  app_id TEXT PRIMARY KEY,
  display_name TEXT NOT NULL,
  description TEXT,
  created_at TEXT NOT NULL
);
