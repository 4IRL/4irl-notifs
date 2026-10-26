# 4irl-notifs

Self-hosted notification hub for the 4IRL app family, built on [ntfy](https://ntfy.sh).

## Components

- **ntfy** — self-hosted notification server (topics, users, ACLs)
- **provisioning-api** — barebones Go service for parametric user/topic provisioning across apps
- **delivery-api** — generic delivery service (Go) backed by its own Postgres (`delivery-postgres`,
  schema applied by the one-shot `delivery-migrate`); serves `/healthz`, `/readyz`, the app message
  API (`/v1/messages`, including `DELETE /v1/messages` cancel of scheduled messages), the operator
  `/v1/admin` registry API (destinations and routes, AES-256-GCM sealed secrets, masked reads), the
  operator delivery log and replay (`/v1/admin/deliveries`) and the operator scheduled-message list
  and cancel (`/v1/admin/scheduled-messages`); takes heartbeat pings (`/v1/heartbeats`) with an operator
  CRUD (`/v1/admin/heartbeats`); delivers webhook, Discord and ntfy messages through an
  in-process delivery worker, releases scheduled messages with an in-process releaser, raises heartbeat
  alerts with an in-process heartbeat checker and prunes old messages with a retention sweeper; see
  [`ARCHITECTURE.md`](ARCHITECTURE.md#delivery-api-endpoints)
- **watchdog** — Cloudflare cron Worker that probes delivery-api, provisioning-api and ntfy from outside
  the VPS and posts state changes to Discord
- **web** — admin UI (Cloudflare Pages, behind Cloudflare Access)

## Why build on it

- **One delivery path, not one per app:** retries with backoff, signing, SSRF-guarded outbound calls,
  idempotent resubmits, dead-lettering and a delivery log with replay, built once for every app.
- **Fewer secrets in every repo:** routine alerts no longer need each app to hold a Discord webhook, and
  rotating one happens in a single place.
- **Scheduling and monitoring included:** `deliver_at` for cancelable deferred sends, and heartbeats that
  turn a silently stopped cron into one alert and one recovery.
- **Watched from outside:** an independent Cloudflare Worker probes the platform every 5 minutes, so an
  outage of it does not go quietly.
- **Cheaper with every app:** a new sink or a stricter safety rule is built once and every integrated
  app inherits it; joining costs a service token, a manifest and one deploy step in the app's repo, one
  operator approval, and a `curl`.

The full case, and the step-by-step path for onboarding a new app, is in
[`docs/app-integration-guide.md`](docs/app-integration-guide.md).

## Local stack

```bash
make local-up
```

`make local-up` first generates the throwaway dev OIDC signing keys (`make dev-oidc-keys`), then
starts the stack; stop it with `make local-down`. The raw form,
`docker compose --project-directory . -f docker-compose.yml up -d`, needs `make dev-oidc-keys` run
first on a fresh checkout (docker-compose.yml bind-mounts `.local/dev-oidc/jwks.json`). ntfy listens on `http://127.0.0.1:8090` by default
(override with `NTFY_PORT`; config: `ntfy/server.yml`; auth database on the `ntfy-auth` named
volume, shared with the provisioning-api container). The same stack also runs `delivery-postgres`,
`delivery-migrate` and `delivery-api` (`http://127.0.0.1:8300` by default, override with
`DELIVERY_PORT`; Postgres data on the `delivery-pgdata` volume). It also runs a local-only
`person-service` (the real Worker under `wrangler dev --local`, no Access, no published port; its
D1 state is lost when the container is recreated); production uses the Cloudflare-hosted Worker.

delivery-api runtime configuration (environment):

| Variable | Default | Purpose |
| --- | --- | --- |
| `DELIVERY_DB_HOST` | required | Postgres host |
| `DELIVERY_DB_PORT` | `5432` | Postgres port |
| `DELIVERY_DB_USER` | required | Postgres user |
| `DELIVERY_DB_NAME` | required | Postgres database name |
| `DELIVERY_DB_PASSWORD` or `DELIVERY_DB_PASSWORD_FILE` | one required | Postgres password, inline or read from a file |
| `DELIVERY_MASTER_KEY` or `DELIVERY_MASTER_KEY_FILE` | one required (serve only; `migrate` ignores it) | Master key sealing destination secrets at rest: exactly 64 lowercase hex characters, inline or read from a file |
| `LISTEN_ADDRESS` | `:8080` | HTTP listen address |
| `DELIVERY_NTFY_URL` | unset | Internal ntfy origin, e.g. `http://ntfy:80`; unset disables the ntfy sink and `to.recipient` delivery |
| `DELIVERY_PROVISIONING_URL` | unset | Internal provisioning-api origin, e.g. `http://provisioning-api:8080` (the host must be exactly `provisioning-api`, any other host is a startup error so the internal credential never leaves the compose network); unset makes `POST /v1/admin/apps/{app_id}/ntfy-publisher` answer 503 |
| `DELIVERY_PROVISIONING_CREDENTIAL` (or `_FILE`) | unset | Internal credential delivery-api sends to provisioning-api as `X-Notifs-Internal-Credential` (64 lowercase hex, shared with provisioning-api's `PROVISIONING_INTERNAL_CREDENTIAL`); unset or unreadable also makes the publisher mint answer 503 and logs one WARN |
| `DELIVERY_RETENTION_DAYS` | `30` | Days a message is kept before the retention sweeper deletes it (integer 1-3650; a message with a `pending` or `scheduled` delivery is never deleted; age counts from the later of `created_at` and `deliver_at`, and a cancelled message ages from `cancelled_at`; an invalid value fails startup) |
| `DELIVERY_MAX_SCHEDULED_PER_APP` | `1000` | Most scheduled (`deliver_at`) messages one app may hold; a new scheduled submit over it is a `429` (integer 1-100000; an invalid value fails startup) |
| `DELIVERY_MAX_SCHEDULED_TOTAL` | `100000` | Most scheduled messages all apps may hold together; a new scheduled submit over it is a `429` `scheduled message capacity reached`, distinct from the per-app `too many scheduled messages` (integer 1-10000000; an invalid value fails startup; the per-app cap is checked first) |
| `DELIVERY_OIDC_AUDIENCE` | `notifs-delivery` | The `aud` a GitHub Actions OIDC token must carry for the app identity exchange (printable non-space ASCII, 1-200 characters). The `notifs-app` action's `audience` input must equal it. Production sets only this variable |
| `DELIVERY_PENDING_IDENTITY_REQUEST_DAYS` | `7` | Days a pending app identity request may go without a refresh (a repeat exchange with the same claims) before the periodic cleanup deletes it, freeing a slot under the 20-request pending cap (integer 1-365; an invalid value, including `0`, fails startup) |
| `DELIVERY_OIDC_DEV_MODE` | unset | **Local and CI only, never production.** `1` allows `DELIVERY_OIDC_JWKS_FILE`; empty, `0` or `1` are the valid values (anything else fails startup) |
| `DELIVERY_OIDC_JWKS_FILE` | unset | **Local and CI only, never production.** A static JWKS file the verifier trusts instead of GitHub's keys (`make dev-oidc-keys` writes one under `.local/dev-oidc/`); setting it without `DELIVERY_OIDC_DEV_MODE=1` fails startup |

provisioning-api in-app checks (environment; see `ARCHITECTURE.md` "In-app checks"):

| Variable | Default | Purpose |
| --- | --- | --- |
| `PROVISIONING_INTERNAL_CREDENTIAL` or `PROVISIONING_INTERNAL_CREDENTIAL_FILE` | unset | Internal credential every route except `GET /healthz` expects in `X-Notifs-Internal-Credential` (64 lowercase hex, shared only with delivery-api's `DELIVERY_PROVISIONING_CREDENTIAL`); never logged |
| `PROVISIONING_INTERNAL_CREDENTIAL_MODE` | `enforce` | `enforce` (a missing or wrong credential answers 401) or `log-only` (WARN `internal credential would reject`, request proceeds); any other value fails startup; `enforce` with no credential loaded fails startup. Local and prod compose both set `log-only` |
| `PROVISIONING_APP_GRANT_MODE` | `enforce` | `enforce` (reset-password, set-password and deprovision answer 404 when the person holds no grant in the calling `app_id`) or `log-only` (WARN `app grant check` with `decision=would_reject`, request proceeds); any other value fails startup. Local and prod compose both set `enforce` |

The live per-app cap (`429`) integration case needs a small cap (2 to 5) on the stack, and skips otherwise.
Start the stack and run the suite with the same value:
`make local-up DELIVERY_MAX_SCHEDULED_PER_APP=2`, then
`make delivery-integration-test DELIVERY_MAX_SCHEDULED_PER_APP=2`.
The CI integration job starts its stack with a cap of 2, so the case runs there.

Several checkouts can run side by side as git worktrees (`make worktree-new`), each with its own
compose project and ports; see [`docs/worktrees.md`](docs/worktrees.md). In a worktree, start the
stack only via `make local-up` / `make local-down`, never raw `docker compose`.

## Topic namespace & auth model

- **`auth-default-access: deny-all`** — every topic grant is explicit; anonymous publish and
  subscribe are rejected.
- **Per-person topics are `{app_id}-{person_hash}-all`** (the standard channel, e.g.
  `urls4irl-76gzqgp4byjl6dje-all`), plus a shared `{app_id}-broadcast` topic per app. Topics need no
  pre-creation — they materialize on first publish/subscribe.
- **One global ntfy user per person** (not one per app). The app's publisher writes `{app_id}-*`;
  each subscriber reads only its own `{app_id}-{person_hash}-*`. See
  [`ARCHITECTURE.md`](ARCHITECTURE.md) for the full model.
- Users authenticate with per-user access tokens (`ntfy token`), created and revoked by the
  provisioning-api via the documented ntfy CLI against the shared auth database.

## Production

Deployed to the shared VPS as an independent docker-compose stack (`docker-compose.prod.yml`)
by `.github/workflows/prod-build-and-deploy.yml` on merge to `main`; the admin UI deploys via
Cloudflare Pages. Public ingress, Cloudflare Access gating, and all required secrets are
provisioned by hand — see [`ARCHITECTURE.md`](ARCHITECTURE.md) ("Deploy pipeline",
"Hosts & DNS", "Authentication") for the complete human-in-the-loop checklist.

See `plans/` for design docs and implementation plans (not tracked in git).
