# ops/backup

Nightly offsite backup of the notification hub (delivery Postgres dump and ntfy `auth.db`) to Cloudflare R2 with restic.
Operating guide, restores and the rebuild order: [`RUNBOOK.md`](RUNBOOK.md). Design: `ARCHITECTURE.md` "Backups".

## Layout

| File                                   | Purpose                                                                          |
| -------------------------------------- | -------------------------------------------------------------------------------- |
| `backup.sh`                            | The nightly job (phases, lock, forget guard, Discord notice, heartbeat ping)     |
| `restore.sh`                           | By-hand restores: `list`, `db --into`, `ntfy-auth --out`                          |
| `install-backup.sh`                    | Host installer: `install`, `status`, `verify`, `run-now`, `init-repo`            |
| `fetch-pinned.sh`, `pins.env`          | Pinned shellcheck and restic downloads; `pins.env` is the one definition of pins |
| `notifs-backup.service`, `.timer`      | systemd units (03:35 America/Los_Angeles)                                        |
| `window.env`                           | The expected run window, checked by `verify`                                     |
| `RUNBOOK.md`                           | Setup, daily checks, restores, rebuild order, drill                              |
| `test/`                                | Bash tests (`lib.sh` helpers, PATH stubs; `run.sh` runs them)                    |

Runtime constants (Postgres container, sqlite image, volume names) live in `backup.sh` only; download pins live in
`pins.env` only.

## Tests

- `make backup-lint`: pinned shellcheck over the scripts here and under `ops/notifs-ops/`.
- `make backup-test`: the offline tests (every `test/test_*.sh` except the live ones), including `test_docs.sh`, which
  statically checks the runbook (real make targets and modes, no webhook, real R2 host, `dak_` key or account id).
- `make backup-test-live`: the docker round trip against throwaway Postgres, sqlite and a local restic repository.
  `REQUIRE_DOCKER=1` fails instead of skipping.

## Host commands

`make host-backup HOST=<ssh-alias> ARGS="install|status|verify|run-now|init-repo"` (interactive sudo) and
`make host-backup-status HOST=<ssh-alias>` (no sudo).
