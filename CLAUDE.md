# CLAUDE.md

Guidance for Claude Code when working in this repository.

## Claude Config

<!-- Consumed by the stronghold's central generic skills (see /Users/ggpropersi/code/CLAUDE.md).
     Stable keys — do not rename. Account-specific GraphQL IDs are intentionally NOT inlined here
     (secrets policy); the genericized workflow resolves them at runtime by name. This project is
     early-stage: the Go module and Vite project are not yet scaffolded, so several keys are TODO. -->

- **Repo slug:** `TODO (no git remote configured yet — no origin in .git/config; likely 4IRL/4irl-notifs, confirm before pushing)`
- **Default branch:** `main` (only local branch; no remote yet)
- **Plans/reviews layout:** `plans/<topic>/` (design docs, plans, and reviews all live under `plans/`, gitignored — not tracked in git)
- **Bot identity:** n/a (no GitHub-App bot set up yet)
- **Bot push script:** n/a
- **Token generator:** n/a
- **Container runtime:** `docker compose` — local stack is ntfy + provisioning-api; TODO (compose file not yet scaffolded)
- **App URL (Playwright MCP):** TODO (React/Vite admin UI in `web/` not yet scaffolded; prod is Cloudflare Pages behind Cloudflare Access)
- **Test login:** n/a (admin UI is behind Cloudflare Access Google/GitHub OAuth; per-app callers use Cloudflare Access Service Tokens)
- **Commands:** TODO — fill in once the Go module (`provisioning-api/`) and Vite project (`web/`) are scaffolded. Expected shape:
  | Purpose | Command |
  |---|---|
  | Integration tests | TODO |
  | Go tests | `go test ./...` (in `provisioning-api/`) |
  | UI/e2e tests | `npx playwright test` (in `web/`) — TODO |
  | JS/unit tests | `npm test` (in `web/`, Vitest) — TODO |
  | Build | `npm run build` (in `web/`, Vite → `dist/`) — TODO |
  | Lint / format | `golangci-lint run` (Go); `npx eslint .` / `npx prettier --write .` (in `web/`) — TODO |
- **GitHub project board:** n/a
- **Issue labels:** resolve at runtime via `gh label list` (do not invent labels)
- **PR reviewer:** n/a

## Project Overview

`4irl-notifs` is a self-hosted notification hub for the 4IRL app family, built on
[ntfy](https://ntfy.sh). See `plans/notification-service/notification-service-design.md` for the
full design (problem, decisions, architecture).

Components:
- **ntfy** — self-hosted pub/sub notification server (topics, users, ACLs), deployed via Docker.
- **provisioning-api** — barebones Go service that shells out to the `ntfy` CLI to expose
  `provision(app_id, user_id)` / `deprovision(app_id, user_id)`. Callable by the admin UI and by
  individual consuming apps, each authenticated via its own Cloudflare Access Service Token.
- **web** — React/Vite admin UI, deployed to Cloudflare Pages behind Cloudflare Access
  (Google/GitHub OAuth).

## Environments

- **local** — docker-compose stack on the developer's own machine (ntfy + provisioning-api).
  Fully testable without deploying anything.
- **production** — the same VPS `urls4irl` deploys to, as its own independent docker-compose
  stack (not merged into u4i's compose file). No separate remote staging tier.

## Development Commands

TODO: fill in once the Go module and Vite project are scaffolded. Expected shape:

| Command | Description |
|---|---|
| `docker compose up -d` | Start the local stack (ntfy + provisioning-api) |
| `docker compose down` | Stop the local stack |
| `go test ./...` (in `provisioning-api/`) | Run Go tests |
| `golangci-lint run` (in `provisioning-api/`) | Lint the Go service |
| `npm test` (in `web/`) | Run frontend unit tests (Vitest) |
| `npm run build` (in `web/`) | Production build (Vite → `dist/`) |
| `npx eslint .` / `npx prettier --write .` (in `web/`) | Lint / format the frontend |

## Testing

- **TDD is required** for the Go provisioning API: Red (failing test for one requirement) →
  Green (minimum code to pass) → Refactor. Do not bulk-code then bulk-test.
- **Go**: table-driven tests via the standard `testing` package.
- **Frontend — logic/state**: Vitest with JSDOM.
- **Frontend — critical flows**: Playwright.
- Every plan's final phase must run the full test suite (Go + frontend) before being marked done.

## Dependency Pinning

All versions in any language must be pinned to exact versions — no ranges, no carets, no tildes.

| Manifest | Required form | Forbidden forms |
|---|---|---|
| `go.mod` | `require pkg v1.2.3` (Go modules pin exact versions by default via `go.sum`) | N/A — verify no `// indirect` drift left unpinned after `go mod tidy` |
| `web/package.json` direct deps & devDeps | `"pkg": "1.2.3"` | `^1.2.3`, `~1.2.3`, `>=`, `*`, `latest` |

## `.claude/`, `CLAUDE.md`, and `.gitignore`

Files under `.claude/` (skills, scripts, settings), `CLAUDE.md`, and `.gitignore` may be committed
and pushed on **any branch**, regardless of the branch topic — never exclude them for being
"unrelated."

## Squash-Merge Branch Hygiene

After a PR is squash-merged into `main`, never continue working on the same branch — the old
branch's commits become stale duplicates. Always `git checkout main && git pull`, then create a
new branch for the next topic.

Before any push: run `git cherry origin/main HEAD`. If any commit shows `-` (already in main),
rebase before pushing.

## GitHub Issue Linking

Every plan (and every design doc, if used) has a linked GitHub issue carrying the public-facing
WHY. Plans/master plans write `github_issue:` / `github_issue_url:` into their YAML frontmatter;
pushes append `Closes #<N>` to the PR body so the issue auto-closes on merge.

## Project Structure

- `plans/` — design docs, plans, and reviews (gitignored, not tracked in git).
- `changelog/` — dated changelog entries (gitignored, not tracked in git).

## Code Style

- Go: descriptive variable names (no single-letter identifiers except conventional loop/receiver
  idioms), typed everywhere, standard `gofmt` formatting.
- TypeScript: never hardcode user-facing strings inline where a shared strings module would serve
  multiple call sites; destructured object params for any function taking 2+ parameters.
