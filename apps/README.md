# App manifests

A manifest is a JSON file that declares one app's (or the platform's) delivery-api notification
setup: the destinations it owns, the routes that send to them, and the heartbeat checks it pings.
`scripts/onboard.mjs` reads it and can `plan` (show what would change), `apply` (create or patch
the real destinations/routes/checks to match), `verify` (send a real test message per channel and
a real ping per heartbeat) or `lint` (check every manifest here, offline, no network call — this
is what `make onboard-lint` runs on every pull request).

## Where manifests live

Each app's own manifest lives in **that app's own repository**, not here. The app's deploy applies
it with the app's delivery key against its app-scoped API
(`node scripts/onboard.mjs apply --manifest <path> --target app`, run by the `notifs-app` action),
so an app manages only its own destinations, routes and heartbeat checks. The manifest can have any
file name and sit anywhere in the app repo; `node scripts/onboard.mjs lint --manifest <path>
--target app` checks its schema offline.

This directory keeps the **`_platform.json`** manifest, which stays operator-applied
(`make onboard app=_platform`). The `<app_id>.json` form below is still accepted here by
`make onboard` and `make onboard-lint` for an operator-applied app, but it is no longer the route
for a new app.

## The two file kinds

- `<app_id>.json` — an **app manifest**. One file per app; the file's basename must equal its own
  `app_id`.
- `_platform.json` — the **platform manifest** (optional, at most one). It is the only file whose
  routes may use the `*` channel pattern, and its owned destinations are prefixed `platform_`
  instead of `<app_id>_`.

No other file belongs in this directory. A stray non-`.json` file or another `_`-prefixed file is
a lint error; this `README.md` is the only exception.

## No secret values here, ever

A manifest's `secrets` entries are always a reference to an environment variable —
`{ "secrets": { "webhook_url": { "env": "MYAPP_OPS_WEBHOOK" } } }` — never a pasted literal value.
`lint` rejects a literal string secret, and the actual secret values are only ever read from the
operator's own shell/password-manager environment when running `plan`/`apply`/`verify`.

See "Onboarding with a manifest (recommended)" in
[`docs/app-integration-guide.md`](../docs/app-integration-guide.md) for the full schema, the
`ONBOARD_*` environment variables, and the plan/apply/verify/prune workflow.
