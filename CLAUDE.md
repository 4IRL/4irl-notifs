# CLAUDE.md

Guidance for Claude Code when working in this repository.

## Claude Config

<!-- Consumed by the stronghold's central generic skills (see ~/code/CLAUDE.md).
     Stable keys — do not rename. Account-specific GraphQL IDs are intentionally NOT inlined here
     (secrets policy); the genericized workflow resolves them at runtime by name. -->

- **Repo slug:** `4IRL/4irl-notifs`
- **Default branch:** `main` (synced with `origin/main`)
- **Plans store (central):** `~/code/plans/4irl-notifs/{open,completed,research}/<topic>/` — plans now live in the central stronghold store (tracked in git for the first time), not this repo (bucket = the `Plans bucket` key below, `4irl-notifs`). Reviews/research/mocks co-located per plan under its `<topic>/`; finished topics move `open/`→`completed/` as a unit. See `~/code/CLAUDE.md` "Central Plans Store". (Legacy in-repo `plans/` migrated 07-2026.)
- **Plans bucket:** `4irl-notifs`
- **Bot identity:** `gpropersi-claude[bot]` `141576524+gpropersi-claude[bot]@users.noreply.github.com`  <!-- consolidated shared bot; replaced repo-local 4irl-notifs-claude -->
- **Bot push script:** `~/code/.claude/scripts/gh-app-push.sh` (central; uses GIT_ASKPASS so the token never lands in argv/URL, authenticates as the shared `gpropersi-claude` App and derives the repo from `origin`)
- **Bot gh wrapper:** `~/code/.claude/scripts/gh-bot.sh <args>` — the **central, repo-agnostic** wrapper (shared by every sub-repo). Runs any `gh` subcommand as this repo's bot (e.g. `gh-bot.sh pr create ...`, `gh-bot.sh api repos/4IRL/4irl-notifs ...`) with NO `$(...)` on the command line; it generates + injects the token internally, never printed. Invoke by **absolute path** from inside the repo (a relative `.claude/scripts/...` resolves to the sub-repo, not the central copy). Prefer this over inline `GH_TOKEN=$(...) gh ...`. Allowlisted (scoped) for `pr *`, `issue *`, `api graphql`, `api repos/*`, `label list`; `pr merge` is denied (the bot must not auto-merge to `main` — that triggers the deploy pipeline).
- **Token generator:** `~/code/.claude/scripts/generate-gh-token.sh` — the central generator (shared `gpropersi-claude` App; auto-resolves this repo's installation from its owner). The repo-local `.claude/bot/generate-gh-token.sh` delegate takes precedence over it (central `gh-app-push.sh` / `gh-bot.sh` resolve it first). Only the shared private key `~/.claude/u4i-app.pem` lives outside git.
- **Container runtime:** `docker compose -p $(COMPOSE_PROJECT_NAME) --project-directory . -f docker-compose.yml` (local stack: ntfy + provisioning-api + `delivery-postgres` + `delivery-migrate` + `delivery-api`; the project name and host ports come from the Makefile, defaulting to `4irl-notifs` / 8090 / 8091 / `DELIVERY_PORT` 8300 and overridden per worktree by `.worktree.env`; in a worktree start the stack only via `make local-up` / `make local-down`)
- **App URL (Playwright MCP):** `http://127.0.0.1:5173/` by default (Vite dev server; the port is `WEB_PORT` from `.worktree.env` in a worktree, run `make worktree-ports`; prod is Cloudflare Pages behind Cloudflare Access)
- **Test login:** n/a (admin UI is behind Cloudflare Access Google/GitHub OAuth; per-app callers use Cloudflare Access Service Tokens)
- **Commands:** (Makefile-first — always prefer `make <target>`; raw command shown for reference)
  | Purpose | Command |
  |---|---|
  | Local stack up / down / logs | `make local-up` / `make local-down` / `make local-logs` |
  | Go tests | `make go-test` (`go test ./...` in `provisioning-api/`) |
  | Go integration tests | `make go-integration-test` (needs local stack up) |
  | Delivery Go tests | `make delivery-test` (`go test ./...` in `delivery-api/`) |
  | Delivery integration tests | `make delivery-integration-test` (needs local stack up; `go test -p 1 -tags integration ./...`) |
  | UI/e2e tests | `make web-e2e` (`npx playwright test` in `web/`) |
  | JS/unit tests | `make web-test` (`npm test` in `web/`, Vitest) |
  | Build | `make web-build` (`tsc -b` + Vite → `dist/`) |
  | Lint / format | `make go-lint` / `make go-fmt` (Go); `make web-lint` / `make web-format` (frontend); `make scripts-lint` / `make scripts-format` (`scripts/`); `make delivery-lint` / `make delivery-fmt` (delivery-api Go) |
  | Workflow YAML lint | `make workflows-lint` (actionlint, pinned; part of `make lint`) |
  | Admin UI dev server | `make dev-web` (foreground) / `make dev-web-bg` + `make dev-web-stop` (detached) |
  | End-to-end smoke test | `make notif-smoke-test` |
  | Help / all lint / all unit tests | `make help` / `make lint` / `make test` |
  | Script tests | `make scripts-test` (`scripts/*.test.mjs`, node:test) |
  | New worktree | `make worktree-new name=<slug> [b=<branch>] [base=<ref>]` |
  | Remove worktree | `make worktree-rm` (run inside the worktree) |
  | Resolved ports | `make worktree-ports` |
- **Push gate:** (suites a push must pass; first matching row wins per changed path, all matched suites run sequentially)
  | Paths (space-separated globs)                                      | Command                |
  | ------------------------------------------------------------------ | ---------------------- |
  | `provisioning-api/**`                                              | `make go-test`         |
  | `delivery-api/**`                                                  | `make delivery-test`   |
  | `web/**`                                                           | `make web-test`        |
  | `person-service/**`                                                | `make worker-test`     |
  | `scripts/**`                                                       | `make scripts-test`    |
  | `docs/** *.md .claude/** .gitignore`                               | na docs and config only |
  | `**`                                                               | `make test`            |
- **GitHub project board:** n/a
- **Issue labels:** resolve at runtime via `gh label list` (do not invent labels)
- **PR reviewer:** `GPropersi`   <!-- always request as reviewer on every PR -->
- **Worktree policy:** `full`
- **Worktree link:** `web/.dev.vars`
- **Worktree setup:** `n/a` <!-- informational; the owned worktree-new target runs npm ci in web/ and person-service/ itself -->
- **Worktree teardown:** `n/a`
- **Worktree ports:** `NTFY_PORT=8090,API_PORT=8091,WEB_PORT=5173,E2E_PORT=4173,DELIVERY_PORT=8300` <!-- informational: slot-0 / primary defaults; scripts/ports.mjs owns the per-worktree values (8090+2s, 8091+2s, 5273+s, 4273+s, 8300+s) -->
- **Worktree allowed targets:** `n/a`
- **Worktree guarded targets:** `n/a`

## Project Overview

`4irl-notifs` is a self-hosted notification hub for the 4IRL app family, built on
[ntfy](https://ntfy.sh). See `plans/notification-service/notification-service-design.md` for the
full design (problem, decisions, architecture).

Components:
- **ntfy** — self-hosted pub/sub notification server (topics, users, ACLs), deployed via Docker.
- **provisioning-api** — barebones Go service that shells out to the `ntfy` CLI to expose
  `provision(app_id, email)` / `deprovision(app_id, email-or-ntfy-user-id)` (identity is
  email-keyed; the ntfy user derives from the email). Callable by the admin UI and by
  individual consuming apps, each authenticated via its own Cloudflare Access Service Token.
- **delivery-api** — generic delivery service (Go + Postgres, with a one-shot `delivery-migrate`
  container); skeleton only for now.
- **web** — React/Vite admin UI, deployed to Cloudflare Pages behind Cloudflare Access
  (Google/GitHub OAuth).

## Environments

- **local** — docker-compose stack on the developer's own machine (ntfy + provisioning-api + delivery-api with its Postgres).
  Fully testable without deploying anything.
- **production** — the same VPS `urls4irl` deploys to, as its own independent docker-compose
  stack (not merged into u4i's compose file). No separate remote staging tier.

## Development Commands

Makefile-first — always prefer `make <target>` (see `Claude Config → Commands`). Raw commands
shown for reference only.

| Command | Description |
|---|---|
| `make local-up` / `make local-down` / `make local-logs` | Start / stop / follow-logs for the local stack (ntfy + provisioning-api + delivery-postgres / delivery-migrate / delivery-api) |
| `make go-test` (`go test ./...`) | Run Go unit tests |
| `make go-integration-test` (`go test -tags integration ./...`) | Run Go integration tests (local stack must be up) |
| `make go-lint` (`gofmt -l .` + `golangci-lint run`) | Check Go formatting + lint |
| `make delivery-test` (`go test ./...` in `delivery-api/`) | Run delivery-api Go unit tests |
| `make delivery-integration-test` (`go test -p 1 -tags integration ./...`) | Run delivery-api integration tests (local stack must be up) |
| `make delivery-lint` / `make delivery-fmt` (`gofmt -l .` + `golangci-lint run` / `gofmt -w .`) | Check / apply delivery-api Go formatting + lint |
| `make workflows-lint` (`actionlint`, pinned `v1.7.12`) | Lint the GitHub Actions workflow YAML |
| `make web-test` (`npm test`) | Run frontend unit tests (Vitest) |
| `make web-e2e` (`npx playwright test`) | Run frontend e2e tests (Playwright) |
| `make web-build` (`npm run build`) | Production build (`tsc -b` + Vite → `dist/`) |
| `make web-lint` / `make web-format` (`npx eslint` / `npx prettier`) | Lint / format the frontend |
| `make dev-web` / `make dev-web-bg` + `make dev-web-stop` | Admin UI dev server (foreground / detached) |
| `make notif-smoke-test` | End-to-end provision → publish → deliver smoke test |
| `make help` | List all targets |
| `make lint` | Run all linters (both Go modules, frontend, scripts, workflow YAML) |
| `make test` | Run all unit tests (both Go modules, web Vitest, worker Vitest, scripts) |
| `make scripts-test` (`node --test "scripts/*.test.mjs"`) | Run the `scripts/` unit tests |
| `make scripts-lint` / `make scripts-format` (`npx prettier` with `web/.prettierrc`) | Check / format `scripts/*.mjs` |
| `make worktree-new name=<slug> [b=<branch>] [base=<ref>]` | Create an isolated worktree with its own compose project and ports (see [`docs/worktrees.md`](docs/worktrees.md)) |
| `make worktree-rm` | Remove the current worktree (run inside it; branch kept) |
| `make worktree-ports` | Print this checkout's resolved ports |

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
