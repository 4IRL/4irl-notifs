# 4irl-notifs

Self-hosted notification hub for the 4IRL app family, built on [ntfy](https://ntfy.sh).

## Components

- **ntfy** — self-hosted notification server (topics, users, ACLs)
- **provisioning-api** — barebones Go service for parametric user/topic provisioning across apps
- **web** — admin UI (Cloudflare Pages, behind Cloudflare Access)

See `plans/` for design docs and implementation plans (not tracked in git).
