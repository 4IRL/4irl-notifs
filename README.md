# 4irl-notifs

Self-hosted notification hub for the 4IRL app family, built on [ntfy](https://ntfy.sh).

## Components

- **ntfy** — self-hosted notification server (topics, users, ACLs)
- **provisioning-api** — barebones Go service for parametric user/topic provisioning across apps
- **delivery-api** — generic delivery service (Go) backed by its own Postgres (`delivery-postgres`,
  schema applied by the one-shot `delivery-migrate`); skeleton only for now
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

Several checkouts can run side by side as git worktrees (`make worktree-new`), each with its own
compose project and ports; see [`docs/worktrees.md`](docs/worktrees.md). In a worktree, start the
stack only via `make local-up` / `make local-down`, never raw `docker compose`.

## Topic namespace & auth model

- **`auth-default-access: deny-all`** — every topic grant is explicit; anonymous publish and
  subscribe are rejected.
- **Topics are namespaced `{app_id}-{channel}`** (e.g. `urls4irl-alerts`). Topics need no
  pre-creation — they materialize on first publish/subscribe.
- **One global ntfy user per person** (not one per app). Provisioning a user into an app grants
  the native wildcard ACL `{app_id}-*` (read-write), so one credential spans every channel of
  every app that user belongs to — and nothing else.
- Users authenticate with per-user access tokens (`ntfy token`), created and revoked by the
  provisioning-api via the documented ntfy CLI against the shared auth database.

## Production

Deployed to the shared VPS as an independent docker-compose stack (`docker-compose.prod.yml`)
by `.github/workflows/prod-build-and-deploy.yml` on merge to `main`; the admin UI deploys via
Cloudflare Pages. Public ingress, Cloudflare Access gating, and all required secrets are
provisioned by hand — see [`ARCHITECTURE.md`](ARCHITECTURE.md) ("Deploy pipeline",
"Hosts & DNS", "Authentication") for the complete human-in-the-loop checklist.

See `plans/` for design docs and implementation plans (not tracked in git).
