# 4irl-notifs

Self-hosted notification hub for the 4IRL app family, built on [ntfy](https://ntfy.sh).

## Components

- **ntfy** — self-hosted notification server (topics, users, ACLs)
- **provisioning-api** — barebones Go service for parametric user/topic provisioning across apps
- **delivery-api** — generic delivery service (Go) backed by its own Postgres (`delivery-postgres`,
  schema applied by the one-shot `delivery-migrate`); serves `/healthz`, `/readyz`, the app message
  API (`/v1/messages`), the operator `/v1/admin` registry API (destinations and routes, AES-256-GCM
  sealed secrets, masked reads) and the operator delivery log and replay
  (`/v1/admin/deliveries`); delivers webhook, Discord and ntfy messages through an in-process delivery worker and prunes old
  messages with a retention sweeper; see
  [`ARCHITECTURE.md`](ARCHITECTURE.md#delivery-api-endpoints)
- **web** — admin UI (Cloudflare Pages, behind Cloudflare Access)

## Local stack

```bash
docker compose --project-directory . -f docker-compose.yml up -d
```

Or `make local-up` / `make local-down`. ntfy listens on `http://127.0.0.1:8090` by default
(override with `NTFY_PORT`; config: `ntfy/server.yml`; auth database on the `ntfy-auth` named
volume, shared with the provisioning-api container). The same stack also runs `delivery-postgres`,
`delivery-migrate` and `delivery-api` (`http://127.0.0.1:8300` by default, override with
`DELIVERY_PORT`; Postgres data on the `delivery-pgdata` volume).

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
| `DELIVERY_PROVISIONING_URL` | unset | Internal provisioning-api origin, e.g. `http://provisioning-api:8080`; unset makes `POST /v1/admin/apps/{app_id}/ntfy-publisher` answer 503 |
| `DELIVERY_RETENTION_DAYS` | `30` | Days a message is kept before the retention sweeper deletes it (integer 1-3650; a message with a pending delivery is never deleted; an invalid value fails startup) |

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
