# 4irl-notifs

Self-hosted notification hub for the 4IRL app family, built on [ntfy](https://ntfy.sh).

## Components

- **ntfy** — self-hosted notification server (topics, users, ACLs)
- **provisioning-api** — barebones Go service for parametric user/topic provisioning across apps
- **web** — admin UI (Cloudflare Pages, behind Cloudflare Access)

## Local stack

```bash
docker compose --project-directory . -f docker-compose.yml up -d
```

ntfy listens on `http://127.0.0.1:8090` (config: `ntfy/server.yml`; auth database on the
`ntfy-auth` named volume, shared with the provisioning-api container).

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
provisioned by hand — see [`docs/deploy-runbook.md`](docs/deploy-runbook.md) for the complete
human-in-the-loop checklist.

See `plans/` for design docs and implementation plans (not tracked in git).
