# Worktrees

4irl-notifs supports running several checkouts side by side as git worktrees under
`.claude/worktrees/<slug>`. Each worktree gets its own compose project (containers, volumes, image)
and its own host ports, so local stacks and Playwright runs in different worktrees do not collide.
The tooling is standalone (no stronghold needed): `make worktree-new` and `make worktree-rm`.

## Primary clone first

Keep the primary clone (`4irl-notifs/`) set up first. It uses the default compose project
`4irl-notifs` and the default ports (ntfy 8090, API 8091, Vite 5173, Playwright preview 4173,
delivery 8300), and it holds the one untracked config file that is symlinked into every worktree:

- `web/.dev.vars` (copy from `web/.dev.vars.example`)

If the primary has no `web/.dev.vars`, the new worktree gets a copy of `web/.dev.vars.example`
instead and `make worktree-new` says so. A real file already present in the worktree is never
overwritten.

## Create

Run from the primary clone:

```sh
make worktree-new name=<slug> [b=<branch>] [base=<ref>]
```

- `name` is the worktree directory slug (lowercased, non `[a-z0-9-]` characters become `-`, max 40
  chars). `b` defaults to `name`; `name` defaults to the slug of `b`.
- A new branch is cut from `origin/<Default branch>` (`origin/main`, resolved from `origin/HEAD`)
  unless `base=<ref>` says otherwise. Use `base=<ref>` to cut from an unmerged branch, for example
  `base=infra/worktree-adoption`: the default base does not contain commits that have not reached
  `origin/main`. If the branch already exists locally or at `origin/<branch>`, it is checked out
  instead. A missing base ref fails before anything is created; run `git fetch origin` or pass an
  existing `base=`.
- After creation the target links `web/.dev.vars`, claims a port slot (below), writes
  `.worktree.env`, then runs `npm ci` in `web/` and `person-service/`. It does not start a stack.

If setup fails, the worktree is left in place for recovery. The error prints the exact commands:

```sh
npm --prefix <path>/web ci && npm --prefix <path>/person-service ci   # retry setup
make -C <path> worktree-rm                                            # discard it
```

## How ports are chosen

The primary clone is slot 0 and keeps the defaults: ntfy `8090`, API `8091`, Vite `5173`, Playwright
preview `4173`, delivery `8300`.

Each worktree gets one slot `s` in `1..99`, and its ports are:

| Key             | Formula (slots 1..99) | Range      |
| --------------- | --------------------- | ---------- |
| `NTFY_PORT`     | `8090 + 2s`           | 8092..8288 |
| `API_PORT`      | `8091 + 2s`           | 8093..8289 |
| `WEB_PORT`      | `5273 + s`            | 5274..5372 |
| `E2E_PORT`      | `4273 + s`            | 4274..4372 |
| `DELIVERY_PORT` | `8300 + s`            | 8301..8399 |

- The ntfy and API ports are adjacent (8090 and 8091), so a step of 1 per slot would make slot `s`'s
  API port equal slot `s+1`'s ntfy port. A step of 2 cannot overlap by construction.
- The web pair is offset by 100 from the defaults so worktree ports stay out of the 5173/4173
  defaults and out of the 5174+/4174+ range that other Vite apps (and the primary's own neighbouring
  dev servers) auto-increment into.
- The delivery range `8300..8399` sits clear of the ntfy/API range (8090..8289) by construction.
  `delivery-postgres` also publishes a local-only test port on `127.0.0.1` at `DELIVERY_PORT + 10000`
  (derived by the Makefile as `DELIVERY_DB_PORT_HOST`, not a `.worktree.env` key; 18301..18399 for
  worktrees, 18300 for the primary), used by the `pgstore` integration test.
- The start slot comes from `crc32('4irl-notifs:' + slug)`. The repo-name salt keeps this repo's slots
  from lining up with other repos that hash the slug alone (tasktracker).
- Slot 0 is never handed out, and slots recorded by sibling worktrees (`NOTIFS_SLOT` in their
  `.worktree.env`) are skipped. A candidate is also rejected if any of its ports equals a port of a
  claimed slot. If the hashed slot is taken or a port is busy, the walk continues to the next slot,
  wrapping within `1..99`; exhausting all 99 fails with a message pointing at the explicit overrides.
- Claiming a slot is serialized by an exclusive lock directory,
  `<git-common-dir>/notifs-worktree-slot.lock`. An overlapping `make worktree-new` fails fast with a
  `slot lock held` message. A stale lock (dead holder, or no pid file for more than 5 seconds) is
  cleared automatically; if the message persists and no other `worktree-new` is running, remove that
  directory by hand.
- The port probe binds `0.0.0.0`, so it is IPv4-only and does not see `::`-only listeners. The
  claimed-slot set is the main separation between worktrees; the strict-port failures (Vite
  `--strictPort`, Playwright `reuseExistingServer: false`, compose's "port is already allocated") are
  the backstop when something unrelated holds a port.

The result is recorded in the worktree's gitignored `.worktree.env`, plain `KEY=VALUE` lines (also
usable by compose `--env-file`):

```
SLUG=<slug>
PRIMARY_ROOT=<path>
IS_PRIMARY=0
NOTIFS_SLOT=<slot>
COMPOSE_PROJECT_NAME=4irl-notifs-<slug>
NTFY_PORT=<port>
API_PORT=<port>
WEB_PORT=<port>
E2E_PORT=<port>
DELIVERY_PORT=<port>
```

Precedence is defaults < `.worktree.env` < environment < `make VAR=...` on the command line:

- The Makefile applies it with `ifndef` guards (it reads single keys from `.worktree.env`, never
  `-include`s it, so an environment value always beats the file) and exports `COMPOSE_PROJECT_NAME`,
  `NTFY_PORT`, `API_PORT`, `WEB_PORT`, `E2E_PORT`, `DELIVERY_PORT` and `DELIVERY_DB_PORT_HOST` to
  compose and child processes.
- `web/vite.config.ts` and `web/playwright.config.ts` read `../.worktree.env` through
  `web/worktree-ports.ts` (defaults < file < env, same order), so a bare `npm run dev` or
  `npx playwright test` in a worktree also gets the right ports.

To see what a checkout resolves to:

```sh
make worktree-ports
```

Override per run with the environment or the command line, for example `make dev-web WEB_PORT=6000`.

**Backfilling `DELIVERY_PORT` into a pre-existing worktree.** A worktree created before the delivery
service has no `DELIVERY_PORT` line in its `.worktree.env`, so it falls back to the primary's default
`8300` and collides with the primary (or with any other un-backfilled worktree). Append the line by
hand using that worktree's own `NOTIFS_SLOT`: `DELIVERY_PORT=<8300 + NOTIFS_SLOT>` (for example slot 94
gives `DELIVERY_PORT=8394`), then confirm with `make worktree-ports`. If the `.worktree.env` has no
`NOTIFS_SLOT` line, pick an unused value in 8301..8399 (check the sibling worktrees' `.worktree.env`
files) rather than computing it. Worktrees created afterwards get it automatically.

## What each worktree isolates

- Compose project `4irl-notifs-<slug>`: its own containers (no `container_name`), named volumes
  (`ntfy-auth`, `ntfy-cache`, `delivery-pgdata`) and locally built provisioning-api and delivery-api
  images. The primary's project `4irl-notifs` and its existing volumes are untouched.
- Host ports (above): ntfy, API, Vite dev server, Playwright preview, delivery.
- The detached dev server's pid and log live in the worktree's own `.dev/` (`.dev/vite-dev.pid`,
  `.dev/vite-dev.log`), not in a shared `/tmp/claude`.
- `node_modules` in `web/` and `person-service/` (installed per worktree).

In a worktree, start and stop the stack only through `make local-up` and `make local-down`. Raw
`docker compose` without `--env-file .worktree.env` would use the project `<slug>` (the directory
basename, not `4irl-notifs-<slug>`) and the default ports 8090/8091, and `worktree-rm` would not clean
it up. Passing `--env-file .worktree.env` does carry `COMPOSE_PROJECT_NAME` and the ports, but keep the
make-only rule so there is one supported path.

## Remove

Run inside the worktree:

```sh
make worktree-rm
```

- It reads the compose project only from the worktree's own `.worktree.env` and requires
  `COMPOSE_PROJECT_NAME` to be `4irl-notifs-<worktree directory basename>`. If the file is missing or
  the value differs, the docker step is skipped with a warning; it never runs against the primary's
  project `4irl-notifs`.
- Otherwise it runs `docker compose ... down -v --rmi local` for that project (docker not running is
  tolerated with a warning).
- If `.dev/vite-dev.pid` exists it runs `make dev-web-stop` in the worktree, before the directory is
  removed (a failure there is only a warning).
- Finally a non-force `git worktree remove`. It also deletes the ignored files inside the worktree
  (`.worktree.env`, `.dev/`, `node_modules`). A dirty tree (tracked changes or untracked,
  non-ignored files) is refused. It refuses the primary clone and any path outside
  `<primary>/.claude/worktrees/`. The branch is always kept; delete it yourself with `git branch -D`.

## Naming rules

- The slug is the directory basename under `.claude/worktrees/`, and also the compose project suffix.
- The slug must not equal `4irl-notifs` or the primary directory's name.
- A slug that already exists under `.claude/worktrees/` is refused.
- A candidate project name `4irl-notifs-<slug>` that already exists in `docker compose ls -a` is
  refused (if docker is down, the check is skipped with a warning).
- One worktree per branch (git enforces this).

## Parallelism limits

- Two full stacks plus Playwright run on the same Docker host and the same machine: memory is the
  practical limit. There is one Docker daemon shared by all worktrees.
- `make web-e2e` no longer reuses a running preview server (`reuseExistingServer` is `false`), so a
  run never attaches to another worktree's server.
- `make dev-pages` runs `wrangler pages dev` on its default listen port (8788; no `--port` is passed),
  which is not parameterized and collides if two run at once.
- `web/.dev.vars` is a shared symlink to the primary's file. Its `PROVISIONING_API_URL` and
  `PERSON_SERVICE_URL` are not rewritten per worktree, so `make dev-pages` in a worktree reaches
  whatever those URLs name, not necessarily that worktree's `API_PORT`.

## Via the stronghold

From `~/code`, `make wt-new REPO=4irl-notifs BRANCH=<branch>` and
`make wt-rm REPO=4irl-notifs BRANCH=<branch>` detect the root Makefile's `worktree-new` /
`worktree-rm` targets and delegate to them.

A repo with no worktree support yet can bootstrap through the stronghold with
`make wt-new REPO=<repo> BRANCH=<branch> INIT=1`, which creates a plain worktree (no owned targets,
ports or compose isolation). That is how this repo's adoption branch was created before it had its own
targets.

## Residual risk

The worktree policy is `full`, so nothing blocks prod-touching targets such as `make worker-deploy`.
They act on production from any worktree exactly as they do from the primary. Run them deliberately.
