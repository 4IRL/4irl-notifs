# CLAUDE.md

Guidance for Claude Code when working in this repository.

## Claude Config

<!-- Consumed by the stronghold's central generic skills (see ~/code/CLAUDE.md).
     Stable keys — do not rename. Account-specific GraphQL IDs are intentionally NOT inlined here
     (secrets policy); the genericized workflow resolves them at runtime by name. -->

- **Repo slug:** `TODO (no git remote configured yet — no origin in .git/config; likely 4IRL/4irl-notifs, confirm before pushing)`
- **Default branch:** `main` (only local branch; no remote yet)
- **Plans/reviews layout:** `plans/<topic>/` (design docs, plans, and reviews all live under `plans/`, gitignored — not tracked in git)
- **Bot identity:** n/a (no GitHub-App bot set up yet)
- **Bot push script:** n/a
- **Token generator:** n/a
- **Container runtime:** `docker compose --project-directory . -f docker-compose.yml` (local stack: ntfy + provisioning-api)
- **App URL (Playwright MCP):** `http://127.0.0.1:5173/` (Vite dev server; prod is Cloudflare Pages behind Cloudflare Access)
- **Test login:** n/a (admin UI is behind Cloudflare Access Google/GitHub OAuth; per-app callers use Cloudflare Access Service Tokens)
- **Commands:**
  | Purpose | Command |
  |---|---|
  | Go tests | `go test ./...` (in `provisioning-api/`) |
  | Go integration tests | `go test -tags integration ./...` (in `provisioning-api/`, needs local stack up) |
  | UI/e2e tests | `npx playwright test` (in `web/`) |
  | JS/unit tests | `npm test` (in `web/`, Vitest) |
  | Build | `npm run build` (in `web/`, `tsc -b` + Vite → `dist/`) |
  | Lint / format | `gofmt -l .` + `golangci-lint run` (in `provisioning-api/`); `npx eslint .` / `npx prettier --check .` (in `web/`) |
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

| Command | Description |
|---|---|
| `docker compose --project-directory . -f docker-compose.yml up -d` | Start the local stack (ntfy + provisioning-api) |
| `docker compose --project-directory . -f docker-compose.yml down` | Stop the local stack |
| `go test ./...` (in `provisioning-api/`) | Run Go unit tests |
| `go test -tags integration ./...` (in `provisioning-api/`) | Run Go integration tests (local stack must be up) |
| `gofmt -l .` (in `provisioning-api/`) | Check Go formatting (no output = clean) |
| `golangci-lint run` (in `provisioning-api/`) | Lint the Go service |
| `npm test` (in `web/`) | Run frontend unit tests (Vitest) |
| `npx playwright test` (in `web/`) | Run frontend e2e tests (Playwright) |
| `npm run build` (in `web/`) | Production build (`tsc -b` + Vite → `dist/`) |
| `npx eslint .` / `npx prettier --write .` (in `web/`) | Lint / format the frontend |

## Testing

- **TDD is required on BOTH sides** — the Go provisioning API AND the React/Vite admin UI:
  Red (failing test for one requirement) → Green (minimum code to pass) → Refactor. Do not
  bulk-code then bulk-test on either side.
- **Go**: table-driven tests via the standard `testing` package.
- **Frontend — logic/state/components**: Vitest with JSDOM + React Testing Library, written
  test-first (a failing component/hook/state test before the implementation).
- **Frontend — critical flows**: Playwright, written test-first for each critical user flow
  before wiring it up.
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
