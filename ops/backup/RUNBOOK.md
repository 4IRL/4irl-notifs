# Backup and restore runbook

How the nightly backup of the notification hub is set up, checked and restored. Modelled on the apps platform runbook
(`apps/platform/backup/RUNBOOK.md` in the apps repo). A root systemd timer on the VPS runs `backup.sh` at 03:35
America/Los_Angeles: it dumps the delivery Postgres database, copies ntfy's `auth.db`, uploads both with restic to a
dedicated Cloudflare R2 bucket (7 daily, 4 weekly, 6 monthly snapshots), posts one Discord notice and pings the
`notifs_ops/nightly_backup` heartbeat. `restore.sh` is the restore tool: it is run by hand as root
(`sudo /opt/notifs-backup/restore.sh ...`) and shares one lock with `backup.sh`, so a restore fails with exit 2 while a
backup runs.

## What is and is not in the backup

- In: the delivery database (`postgres/delivery.dump`) and ntfy's subscriber accounts (`ntfy/auth.db`).
- Not in: anything under `/home/4irl-notifs/secrets-persistent/`. In particular `DELIVERY_MASTER_KEY` and
  `DELIVERY_MASTER_KEY_PREVIOUS` are deliberately kept out of R2. The dump holds sealed secrets (destination
  credentials, app publisher tokens, probe Access credentials) that only open with that key.
- **A restore needs the master key from Infisical** (the same value as the `DELIVERY_MASTER_KEY` GitHub secret). Without
  it the restored rows cannot be opened.
- Not in: `backup.env` itself, the `ntfy-cache` volume, and the `NOTIFS_OPS_DELIVERY_KEY` file (a fresh one is minted by
  the notifs-ops key workflow).

## 1. Setup checklist

1. In the Cloudflare dashboard create **one** dedicated R2 bucket for this backup.
2. Create an **Object Read & Write** API token scoped to that bucket only (no account-wide access).
3. Note the bucket's S3 endpoint, the token's access key id and secret, and create a Discord webhook for the notices.
4. Add five GitHub secrets (repo `4IRL/4irl-notifs`, same place as the other deploy secrets). The prod deploy renders
   them into `/home/4irl-notifs/secrets-persistent/backup.env` (owned by the 4irl-notifs user, mode 0600; the installer
   never writes it). Values are literal with **no quotes, `$`, backticks, backslashes or whitespace** (the deploy
   refuses them, and the reader refuses a leading quote, a carriage return, an unknown key and a duplicate key). All
   five set writes the file; none set leaves it as is; a partial set aborts the deploy before it touches the VPS.

   | GitHub secret | `backup.env` key | Value |
   | ------------- | ---------------- | ----- |
   | `BACKUP_RESTIC_REPOSITORY` | `RESTIC_REPOSITORY` | `s3:https://<account-id>.r2.cloudflarestorage.com/<bucket>` |
   | `BACKUP_RESTIC_PASSWORD` | `RESTIC_PASSWORD` | a long random restic password |
   | `BACKUP_R2_ACCESS_KEY_ID` | `AWS_ACCESS_KEY_ID` | the token access key id |
   | `BACKUP_R2_SECRET_ACCESS_KEY` | `AWS_SECRET_ACCESS_KEY` | the token secret |
   | `BACKUP_DISCORD_WEBHOOK` | `DISCORD_WEBHOOK` | the webhook url |

   `AWS_DEFAULT_REGION=auto` is fixed by the deploy. Then run a prod deploy (merge to `main`, or re-run the deploy
   workflow) and check the file landed: `ssh <ssh-alias> 'stat -c "%U %a" secrets-persistent/backup.env'` should print
   `4irl-notifs 600`.

5. Install the units, scripts and restic (prompts for the sudo password, so run it yourself):
   `make host-backup HOST=<ssh-alias> ARGS="install"`.
6. Initialise the repository once (an explicit installer mode, never automatic):
   `make host-backup HOST=<ssh-alias> ARGS="init-repo"`. It refuses when a repository already exists.
7. Run a first backup and check it: `make host-backup HOST=<ssh-alias> ARGS="run-now"`, then
   `make host-backup HOST=<ssh-alias> ARGS="verify"`.

**Store `RESTIC_PASSWORD`, the R2 token (access key id and secret) and the account id in Infisical.** Without the restic
password the backups cannot be restored and without the R2 credentials they cannot be read. The GitHub secrets are
write-only, so Infisical is the readable copy; the host file is rendered from them on every deploy and is
intentionally not in the backup.

## 2. Daily checks

```sh
make host-backup-status HOST=<ssh-alias>
```

Prints `last-run.tsv` (start, end, `ok` or `failed`, snapshot id, failed phase) and the timer's next run. No sudo. The
Discord notice and the heartbeat tell you the same thing without looking: a failed run posts `failed`, and a run that
never happens makes the `nightly_backup` check go down after its 24 h period plus 12 h grace.

For a full audit (installed files byte-identical to the checkout, owners and modes, restic hash, timer, window, sqlite
image) run `make host-backup HOST=<ssh-alias> ARGS="verify"` (it runs `install-backup.sh verify` on the host). It fails
when `last-run.tsv` is not `ok` or older than 36 h.

## 3. Look at what exists

```sh
sudo /opt/notifs-backup/restore.sh list
```

Prints the snapshots of host `4irl-notifs`. Every restore takes `--snapshot <id>` (a hex id from the list) or defaults
to `latest`.

## 4. Restore the delivery database (single-DB restore)

The restore goes into a **new** database next to the live one, never `delivery`. It never overwrites.

1. Restore into a new name:

   ```sh
   sudo /opt/notifs-backup/restore.sh db --into delivery_restore
   ```

   It prints the row count per table.
2. Check the restored data (for example
   `sudo docker exec -i 4irl-notifs-delivery-postgres psql -U delivery -d delivery_restore`).
3. To swap it in, stop delivery-api first (and anything else holding a connection), then rename both databases from the
   maintenance database `postgres`:

   ```sh
   sudo docker stop 4irl-notifs-delivery-api
   sudo docker exec -i 4irl-notifs-delivery-postgres psql -U delivery -d postgres
   ```

   ```sql
   ALTER DATABASE delivery RENAME TO delivery_old;
   ALTER DATABASE delivery_restore RENAME TO delivery;
   ```

4. Start delivery-api (`sudo docker start 4irl-notifs-delivery-api`) and check `/readyz`. Drop `delivery_old` once you
   are satisfied.

## 5. Restore ntfy's `auth.db`

The restore writes into a new directory outside every docker volume; you swap the file in yourself.

1. Restore to a scratch directory (it must not exist yet or be empty, per the refusal message):

   ```sh
   sudo /opt/notifs-backup/restore.sh ntfy-auth --out /var/lib/notifs-backup/work/auth-restore
   ```

2. Stop ntfy and provisioning-api (both open the file):

   ```sh
   sudo docker stop 4irl-notifs-ntfy 4irl-notifs-provisioning-api
   ```

3. Move the old file aside and place the restored one in the `4irl-notifs_ntfy-auth` volume. Ntfy runs as uid 1000, so
   the file **must** be `chown 1000:1000`:

   ```sh
   sudo docker run --rm -v 4irl-notifs_ntfy-auth:/dst -v /var/lib/notifs-backup/work/auth-restore:/src:ro alpine:3 sh -c 'mv /dst/auth.db /dst/auth.db.old; rm -f /dst/auth.db-wal /dst/auth.db-shm; cp /src/auth.db /dst/auth.db; chown 1000:1000 /dst/auth.db'
   ```

4. Start them again (`sudo docker start 4irl-notifs-ntfy 4irl-notifs-provisioning-api`), check a subscriber can still
   connect, then delete the scratch directory and `auth.db.old`.

## 6. Full rebuild order (a whole new server)

1. **Have the secrets ready from Infisical:** `RESTIC_PASSWORD`, the R2 credentials and bucket endpoint, the Discord
   webhook, and `DELIVERY_MASTER_KEY` (plus `DELIVERY_MASTER_KEY_PREVIOUS` if a rotation was in flight when the
   snapshot was taken: a mid-rotation backup holds rows sealed under both keys, so it needs both).
2. **Deploy the stack.** The deploy rewrites `secrets-persistent/` from GitHub secrets, so `DELIVERY_MASTER_KEY` must be
   present as the GitHub secret. If GitHub lost it, restore it from Infisical first. A different key here would orphan
   every sealed row.
3. Make sure the `BACKUP_*` GitHub secrets are set (section 1, step 4; the step 2 deploy renders `backup.env`), and run `make host-backup HOST=<ssh-alias> ARGS="install"`. Do **not** run
   `init-repo`: the repository already exists and `init-repo` refuses.
4. Stop delivery-api: `sudo docker stop 4irl-notifs-delivery-api`.
5. Restore the dump (section 4, steps 1 to 3), then `auth.db` (section 5).
6. Start delivery-api, ntfy and provisioning-api, then run `verify-keys` from the stack directory:
   `docker compose -f docker-compose.prod.yml run --rm --no-deps delivery-api verify-keys`. It must exit 0.
7. Re-run the notifs-ops key workflow (workflow_dispatch on `main`) so the new host gets a fresh heartbeat key, then
   `make host-backup HOST=<ssh-alias> ARGS="run-now"` and `verify`.

**A restore without the key** (the key is gone from both GitHub and Infisical) follows the existing lost-key recovery in
`ARCHITECTURE.md` ("Recovery" under the master-key rotation runbook): remove routes that reference the unreadable
destinations, recreate them, re-mint app publisher tokens and replace probe credentials.

## 7. Drill procedure

Run it after any change to the scripts and at least every few months. It touches nothing live.

1. `sudo /opt/notifs-backup/restore.sh list`: the newest snapshot is under 36 h old.
2. `sudo /opt/notifs-backup/restore.sh db --into delivery_drill`: compare the printed row counts with the live tables
   (`messages` and `destinations` in particular), then drop it
   (`sudo docker exec -i 4irl-notifs-delivery-postgres psql -U delivery -d postgres -c 'DROP DATABASE delivery_drill'`).
3. `sudo /opt/notifs-backup/restore.sh ntfy-auth --out /var/lib/notifs-backup/work/auth-drill`: it runs
   `PRAGMA integrity_check`; delete the directory afterwards.
4. Confirm the sealed rows open: with the real key deployed, a restored database swapped into a scratch stack passes
   `verify-keys`. Record the date of the drill in your notes.

## 8. Pausing the heartbeat during maintenance

Planned downtime (a rebuild, a long VPS maintenance window) would otherwise raise a false `nightly_backup` alert. Pause
the check in the admin UI (Heartbeats, `notifs_ops/nightly_backup`, Pause) or with the admin API
`POST /v1/admin/heartbeats/{id}/pause`, and resume it with `/resume` afterwards. Also disable the timer if the host will
be down across 03:35: `sudo systemctl disable --now notifs-backup.timer`, and re-enable it with
`make host-backup HOST=<ssh-alias> ARGS="install"` afterwards. `verify` FAILs while the timer is off.

## 9. Troubleshooting

- Exit 2 means a refusal or bad input and nothing was changed; exit 1 names the step that failed.
- `restore: missing directory ...`: run `make host-backup HOST=<ssh-alias> ARGS="install"`; the restore never creates the
  state dirs.
- A half-restored database after a failed `db` restore: the new database `delivery_restore` already exists. Drop it
  before retrying (`sudo docker exec -i 4irl-notifs-delivery-postgres psql -U delivery -d postgres -c 'DROP DATABASE
  delivery_restore'`), because `db` refuses an `--into` name that already exists.
- The Discord notice says `failed ... at phase <p>`: read `/var/log/notifs-backup/<date>.log` on the host for that
  phase's error text. A `lock` failure means another run (or a restore) held the lock; it sends no heartbeat ping and
  leaves `last-run.tsv` untouched (the lock holder reports its own result).
- `warning: heartbeat: ...` in the notice: the backup itself succeeded but the ping did not. The key file
  `/home/4irl-notifs/secrets-persistent/NOTIFS_OPS_DELIVERY_KEY` is missing or malformed, or delivery-api answered
  non-200. If the key write failed after a successful exchange, re-run the notifs-ops key workflow via
  workflow_dispatch on `main` within 24 h: the next exchange mints and delivers a fresh key.
- **A red notifs-ops key run that coincides with a deploy is expected occasionally, so re-run it manually via
  workflow_dispatch on `main`.** There is no need to investigate unless the re-run is also red.
- Leftovers: restore scratch lives under `/var/lib/notifs-backup/work` (root only; it holds restored
  dumps). Delete it when done.

## 10. The pinned VPS host key and the pinned cloudflared

The notifs-ops key workflow (`.github/workflows/notifs-ops-key.yml`) connects to the VPS with
`StrictHostKeyChecking=yes` against a pinned host key, so a changed or impersonated host fails the run instead of being
trusted. The pin is the repository secret `PROD_SSH_KNOWN_HOSTS`: the known_hosts line(s) for the exact `PROD_SSH_HOST`
value (`<host> <key type> <public key>`; a public key, not a credential, but kept as a secret next to the other
`PROD_SSH_*` values). The workflow fails before any exchange when it is unset.

- **Set it (once):** from a trusted machine that already reaches the VPS, run `ssh-keyscan -t ed25519 <PROD_SSH_HOST>`
  through the same Cloudflare Access path (or read `/etc/ssh/ssh_host_ed25519_key.pub` on the VPS itself and prefix it
  with `<PROD_SSH_HOST> `), compare the fingerprint with `ssh-keygen -lf` on the VPS, and store the line as the
  `PROD_SSH_KNOWN_HOSTS` secret in the `production` environment.
- **Rotate on every host rebuild:** a rebuilt or re-imaged VPS gets a new host key, so update `PROD_SSH_KNOWN_HOSTS` as
  part of the rebuild (section 6, the full rebuild order) or the next notifs-ops key run is red with a host key error.
- **cloudflared** is pinned in `ops/backup/pins.env` (`CLOUDFLARED_VERSION` and `CLOUDFLARED_SHA256_AMD64`) and fetched by
  `fetch-pinned.sh`, which refuses a hash mismatch. To bump it, change both lines together to a released tag and the
  sha256 of its `cloudflared-linux-amd64` asset.
