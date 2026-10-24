# Integrating an app with 4irl-notifs

**Audience:** an engineer (or an LLM) wiring a new client application (e.g. `urls4irl`,
`tasktracker`) into the 4IRL notification service so it can send personalized notifications to its
users, deliver alerts through retries and a delivery log, schedule messages for later, and monitor its
recurring jobs. This is a **self-service integration guide** — it is not about deploying the
notification service itself (that is `ARCHITECTURE.md`).

If you are deciding whether to integrate, read **Why integrate**. If you are integrating, read
**Onboarding a new app** first: it says what the operator does, what your app does, and in what order.
If the app already sends its own Discord, webhook or ntfy messages or reports from its own cron jobs, read
**Migrating an existing app from its own senders**: a generic, lane-by-lane playbook with reference
clients in Python and shell.
If you are a program or an LLM agent calling delivery-api, jump to **Agent quick reference**: every
endpoint with its request, response and status codes, and a copy-paste onboarding recipe. For the ntfy
path, also read the **Mental model** and **Integration in 5 steps** sections.

---

## Why integrate (and what it buys us long term)

You do not have to build any of this yourself, and every app that joins makes the next one cheaper.

**What you stop building**

- **A sender per app.** Before delivery-api, each app carried its own sender (Python, Go and bash), its own
  webhook secrets and, mostly, no retries and no record of what was sent. Delivery now lives in one
  hardened place: signing, retries with backoff, SSRF-guarded outbound calls, idempotent resubmits,
  dead-lettering and an audit trail.
- **Webhook URLs in every repo.** Apps stop holding Discord webhook URLs for routine traffic: fewer
  secrets to leak, and one place to rotate them, with an overlap window so receivers keep working during
  a rotation. (Critical alerts still keep one minimal direct fallback; see `critical`.)
- **A scheduler for "later".** `deliver_at` holds a message until it is due and lets you cancel or replace
  it by key. No cron sweep, no queue table.
- **A dead-man's switch for your jobs.** A cron or timer that silently stopped firing used to be noticed
  only when a human noticed something missing. One `curl` at the end of the job now turns that silence
  into exactly one alert, and one recovery alert when the job comes back.

**What you get for free**

- **Visibility.** The Delivery tab of the admin UI shows the delivery log (with dead letters and replay),
  pending scheduled sends and heartbeat status. "Did it send?" becomes a lookup, not a log grep.
- **Safe retries.** `(app_id, idempotency_key)` makes retrying after a timeout harmless: the original
  message comes back flagged `duplicate`.
- **Routing as configuration.** A message sent without `to` fans out through the operator's routes, so
  moving an alert to another Discord channel, adding a second destination or switching a sink needs no
  app change and no deploy.
- **Per-person delivery.** `to.recipient` takes an email, and the same email is the same person across
  every 4IRL app, so you never invent or store a notification identity of your own.
- **An independent watcher on the platform itself.** The watchdog Worker runs on Cloudflare, outside the
  VPS, and probes delivery-api, provisioning-api and ntfy every 5 minutes, posting to its own Discord
  channel. You do not need to build monitoring for the notification path, and an outage of it does not
  go quietly.
- **Nothing extra to host.** Your app needs no inbound endpoint to be monitored: jobs push a ping and
  the platform does the watching.

**Why it compounds**

- **One improvement, every app.** A new sink, a stricter outbound-safety rule or a better retry policy
  is built once and every integrated app inherits it. With per-app senders, each copy has to be fixed
  separately.
- **A flat, small cost to join.** A service token, a manifest in your repo with one deploy step, one
  operator approval, and a `curl`.
  That cost does not grow as the platform gains capabilities; the capabilities grow underneath it.
- **A stable contract.** Apps speak one envelope (`app_id`, `channel`, `payload`, `display`). The
  delivery internals can change without touching them.
- **One place to audit and to fix.** Who sent what, when and with what result is in one delivery log,
  kept for `DELIVERY_RETENTION_DAYS` (30 by default). An incident is debugged once, not once per repo.
- **Failures get noticed by design.** A heartbeat check that is never pinged raises an alert, so a
  half-finished integration surfaces instead of going quiet.

**What it costs (so the trade is honest)**

- **A shared dependency.** Your app now depends on delivery-api being up. Idempotent retries keep
  ordinary sends safe, critical alerts keep a direct fallback, and the watchdog tells the operator when
  the platform is down.
- **A service token and a delivery key to manage.** The Cloudflare Access service token only gets the app
  through the door; the app's own delivery key (`dak_...`, minted by its deploy workflow after one operator
  approval) is what tells delivery-api which app is calling, so one app cannot act as another (see
  "App identity: how an app proves who it is").
- **Operator steps per app.** An operator creates the Access token and approves the app's identity once;
  the app's own deploy creates its destinations, routes and heartbeat checks from its manifest. The
  sections below list exactly what.

---

## Onboarding with a manifest (recommended)

For apps using `POST /v1/messages` and/or heartbeats (most apps — see the table below), the recommended
path is one reviewed JSON manifest plus `scripts/onboard.mjs`, not clicking through the admin UI or
scripting `curl` by hand. The manifest is what you edit; the tool makes the server match it. **Not** a
replacement for the "ntfy direct" path (Steps 1-5) or for the one-time operator setup in "Operator steps
(once per app)" below (the Access split, service tokens, the app's ntfy publisher, approving the app's
identity) — it only covers the destinations/routes/heartbeats part of onboarding.

**Where the manifest lives and who applies it.**
- **An app's own manifest lives in the app's own repo** and is applied by the app's own deploy with the
  app's delivery key, through the `notifs-app` GitHub action (see "Onboarding from the app's own repo"
  below). The app manages only its own destinations, routes and heartbeat checks, through
  `/v1/apps/{app_id}/…`. This is the production path for every app.
- **The `_platform` manifest** (`apps/_platform.json`, wildcard routes and shared `platform_`
  destinations) stays in this repo and is applied by the operator with `make onboard app=_platform`
  (operator proxy token, `/v1/admin`). The `make onboard app=<id>` operator mode also still accepts an
  operator-authored `apps/<app_id>.json` for `plan` and `apply`, but it is no longer the route for a new app.
- **`make onboard app=<id> cmd=verify` is dev/test-only.** Every endpoint `verify` calls needs the app's
  delivery key, and no production mechanism supplies that key to an operator-run command (it works only
  through the `DELIVERY_OIDC_DEV_MODE` token-exchange harness the integration tests use). Never run
  `cmd=verify` against an app manifest in production. The only production-supported verify path for an
  app is `--target app`, run by the app's own deploy.

**Schema summary.** One JSON file per app (in the app repo it can have any file name and sit anywhere; an
operator-authored one under `apps/` has a basename equal to `app_id`):
- `destinations`: either an **owned** destination — `{ "name", "type": "ntfy"|"discord"|"webhook", "config"?, "secrets"?, "enabled"? }`, with `name` required to start with `<app_id>_` — that the tool creates, patches and (with `--prune`) deletes, or a **shared reference** `{ "use": "<name>" }` to a destination owned by another manifest, which the app only routes to and never modifies.
- `routes`: `{ "channel", "destinations", "enabled"? }` — `app_pattern` is always derived as the
  manifest's own `app_id` and is never written by hand.
- `heartbeats`: `{ "name", "period", "grace", "destinations" }`, with `period`/`grace` as durations
  (`24h`, `1h`, …) rather than raw seconds.
- an optional `environments.<env>` block overriding `heartbeats.<name>.{period,grace}` and
  `destinations.<name>.enabled` per environment.

The platform-wide manifest (wildcard `*` routes, `platform_`-prefixed shared destinations) is the
separate `apps/_platform.json`, not an app manifest.

**Commands** (`make onboard app=<app_id|_platform> ...` — see `make help` for every flag):
- `cmd=plan` (the default) — read-only: lists every create/update/delete the manifest implies and
  always exits `0`; a printed `! blocking` line means `apply` would refuse, it never stops `plan`.
- `cmd=apply` — creates/patches in dependency order (destinations, then routes, then heartbeats),
  refuses on any blocking action (a destination type/field change that `PATCH` cannot make, or a
  `use` reference to a destination that does not exist), then re-lists and re-plans to confirm the
  server actually converged.
- `cmd=verify` — sends one real test message through each exact-channel route and pings every
  heartbeat, using the app's own Access token and delivery key, and reports pass/fail per item.
  **Dev/test-only through `make onboard`** (see above); for a real app it runs as `--target app`.
- `prune=1` (with `cmd=plan` or `cmd=apply`) — also reports, or deletes, server-side objects the app
  owns that the manifest no longer lists (heartbeats and routes before destinations, since a
  destination still referenced by either cannot be removed).
- `resend_secrets=1` (with `cmd=apply`) — re-sends every owned destination's secrets from the
  environment; used for secret rotation.
- `make onboard-lint` (also part of `make lint`, and run in CI on every PR) — an offline schema and
  cross-manifest check over all of `apps/`, with no network call.

**Env vars** (set in your own shell or password manager — never committed to the repo):
- `ONBOARD_URL` — the delivery-api base URL to manage. `make onboard` defaults it to the local stack
  (`DELIVERY_URL`) unless you already have it exported, so export it yourself to point at staging or
  production.
- `ONBOARD_ACCESS_ID` / `ONBOARD_ACCESS_SECRET` — the operator proxy token, used by the operator mode's
  `plan`/`apply` (`/v1/admin`).
- `ONBOARD_APP_ACCESS_ID` / `ONBOARD_APP_ACCESS_SECRET` — the app's own Access token. Used by `verify` and
  by `plan`/`apply` with `--target app`; required for a non-loopback `ONBOARD_URL`.
- `ONBOARD_APP_KEY` — the app's delivery key (`dak_...`), sent as `Authorization: Bearer`. Required by `plan`,
  `apply` and `verify` with `--target app` (not by `lint`), even on a loopback URL.

**Ownership rules.** An app's manifest only ever creates, patches or (with `--prune`) deletes
destinations named `<app_id>_*`, routes whose `app_pattern` is exactly its own `app_id`, and
heartbeats whose `app_id` matches. It never touches anything owned by another app's manifest or by
the platform manifest, and a `use` reference never modifies the destination it points at.

**The manifest is the source of truth; admin UI edits show as drift.** Once an app is onboarded with a
manifest, make the manifest the thing you edit. A change made directly in the admin UI shows up as an
`update`/`unmanaged` action the next time anyone runs `plan`, and a later `apply` overwrites it back to
what the manifest says.

**`verify` always pings every heartbeat it checks**, including on a first/dry-run integration check — a
ping marks the check `up` and starts its deadline. `verify` prints a warning naming each heartbeat it
pinged and the timestamp it goes `down` by without a real ping from the app, so confirm the app's own
recurring job is actually wired up to ping before relying on the result.

### App identity: how an app proves who it is

The Cloudflare Access service token is the gate on every call, but it only says "some allowed token". It
does not say which app is calling. delivery-api therefore gives each app its own **delivery key**
(`dak_` plus 43 characters), and requires it as `Authorization: Bearer dak_...` on every app-facing
endpoint: `/v1/messages` (submit, status, cancel), the heartbeat pings and everything under
`/v1/apps/{app_id}/…`. The key's app is the caller; an `app_id` in the body, the query or the path that
differs from it is `403 app_id does not match app key`.

You never create a key by hand. The app's deploy workflow trades a GitHub-signed OIDC token for one:

1. **First deploy: a request, no key.** The exchange verifies the token and records a **pending identity
   request** (the app id you claimed, plus the GitHub-verified repository, workflow file, branch or tag, and
   environment). The deploy fails with "identity request #N awaits operator approval in the admin UI".
2. **Operator approval, once.** The operator approves the request in the admin UI (**App identities**).
   That creates the binding `app_id` ← GitHub repository + workflow file + ref + environment.
3. **Every later deploy: a fresh key.** The exchange issues a new key (shown once, never stored in plain
   text on the server) and the app's previous key keeps working for **24 hours**, then stops. There is no
   other expiry. Each exchange rotates the key, **including dry runs** (`apply: 'false'`), and the action
   exchanges **before** it lints the manifest, so a deploy that then fails on the manifest has already
   rotated the key.
4. **The binding is exact.** A deploy whose repository, workflow file, ref or environment differs from the
   approved binding is `403 identity does not match the approved binding` and files nothing. To change any
   of them (a renamed workflow file, deploying from another branch, adding an `environment:`), ask the
   operator to unbind the app, then deploy again and have the new request approved.

### Onboarding from the app's own repo (the `notifs-app` action)

1. **Put the manifest in your repo** (any path, for example `.notifs/manifest.json`). It follows the
   schema above, with `app_id` set to your app id. Destination secrets are `{ "env": "NAME" }` references,
   never literals; set those environment variables in the deploy job's `env`. `_platform` manifests are not
   accepted here (`--target app takes an app manifest`). A manifest can be checked offline with
   `node scripts/onboard.mjs lint --manifest <path> --target app` from a checkout of this repo (the action
   does it for you); it is a single-file schema check and does not see other apps' manifests, because the
   server enforces cross-app collision safety (name and topic prefixes, ownership).
2. **Store the Access token** the operator gave you as two repository secrets (for example
   `NOTIFS_ACCESS_CLIENT_ID` and `NOTIFS_ACCESS_CLIENT_SECRET`).
3. **Add the action to the deploy workflow.** The job needs `permissions: id-token: write` (GitHub's OIDC
   token) and `contents: read` for the checkout. Pin both actions to a full commit SHA:

   ```yaml
   permissions:
     contents: read
     id-token: write # lets the job request the OIDC token the exchange needs

   jobs:
     deploy:
       runs-on: ubuntu-latest
       environment: production # optional; when set it is part of the approved identity
       steps:
         - uses: actions/checkout@<full commit SHA>
         - id: notifs
           uses: 4IRL/4irl-notifs/.github/actions/notifs-app@<full commit SHA of 4irl-notifs>
           with:
             app_id: myapp
             delivery_url: https://notifs-delivery.4irl.app
             access_client_id: ${{ secrets.NOTIFS_ACCESS_CLIENT_ID }}
             access_client_secret: ${{ secrets.NOTIFS_ACCESS_CLIENT_SECRET }}
             manifest: .notifs/manifest.json
             # apply: 'false'                 # dry run: print the plan only (still mints a key)
             # audience: <the operator's value> # only if DELIVERY_OIDC_AUDIENCE was changed from its default
   ```

   Inputs: `app_id`, `delivery_url`, `access_client_id`, `access_client_secret` (all required);
   `manifest` (optional; without it only the exchange runs); `apply` (default `'true'`);
   `audience` (default `notifs-delivery`, which must equal the server's `DELIVERY_OIDC_AUDIENCE`, so
   pass it only if the operator changed that value). `app_id` must equal the `app_id` in the manifest.
   The action requests the OIDC token, exchanges it,
   masks the key in the logs, lints the manifest, then runs `apply` (or `plan` for any `apply` value other
   than `'true'`) with the new key. The output `steps.notifs.outputs.key` is the new key, already masked. If the 4irl-notifs
   repository is private, its Actions settings must allow your repository to use the action.
4. **First deploy: expect the failure.** The run fails with the pending-request message. Tell the operator;
   once they approve it, re-run the job (a re-run fetches a new OIDC token). The key is issued and the
   manifest is applied.
5. **Store the key on the app host** with the same `_FILE` pattern this repo uses for its own secrets: the
   deploy writes `steps.notifs.outputs.key` to a file that only the app can read (for example a Docker
   Compose secret), the app reads it from a `…_KEY_FILE` environment variable, and it is never put in the
   repository, an image or a log. Every deploy replaces the file. Because the previous key works for 24 h,
   a rolling deploy needs no coordination, but a long-running process that cached the key must reload it
   (or re-read the file after a `401`) within that window. A deploy that exchanged a key and then failed
   before updating the file leaves the host holding a key that stops working 24 h later: re-run the deploy
   before then.
6. **Send the bearer header on every call:**

   ```bash
   curl -fsS -X POST "https://notifs-delivery.4irl.app/v1/messages?wait=10" \
     -H "CF-Access-Client-Id: $NOTIFS_ID" -H "CF-Access-Client-Secret: $NOTIFS_SECRET" \
     -H "Authorization: Bearer $(cat "$NOTIFS_KEY_FILE")" \
     -H "Content-Type: application/json" \
     -d '{"app_id":"myapp","channel":"alerts","idempotency_key":"onboarding-test-1","payload":{},"display":{"title":"myapp is connected","body":"First message"}}'
   ```

To run the same flow by hand (or to verify), the CLI commands are `node scripts/onboard.mjs <plan|apply|verify>
--manifest <path> --target app` and `lint --manifest <path> --target app`. `plan`, `apply` and `verify` need
`ONBOARD_URL`, `ONBOARD_APP_ACCESS_ID`, `ONBOARD_APP_ACCESS_SECRET` and `ONBOARD_APP_KEY` set; `lint` needs
no environment. The action runs `lint`, then
`apply` or `plan`; it does not run `verify`. `--prune` is allowed in this mode (the server already
limits it to the app's own rows).

### What an app may and may not manage

- **Ownership.** Everything an app creates through `/v1/apps/{app_id}/…` is owned by that app, and the
  operator's rows and other apps' rows are invisible to it (`404`, the same as an absent one). The owner
  is set by the server from the key, never read from the body.
- **Usable destinations.** An app's route or heartbeat check can use its own destinations and the
  operator-owned ones, never another app's (`400 unknown destination`).
- **Name rules.** A destination name starts with `<app_id>_`; an ntfy `config.topic` starts with
  `<app_id>-`; an app route's `app_pattern` is always the app's own id.
- **Public URLs only, at registration.** A webhook or Discord URL is resolved when the app registers it;
  a host that resolves to a private address is `400`. The delivery-time check still runs too.
- **Collisions are global.** The prefix rule does not tell app `app` from app `app_a`, and uniqueness keys
  are not per owner: a name, a route (`app_pattern` + `channel_pattern`) or a check (`app_id` + `name`)
  already held by anyone, the operator included, is `409` for the app. The operator resolves it by
  deleting the squatting or conflicting row (see "Operator runbook: app identities"). It is an availability
  collision, never a way to deliver to or read another app's data.

---

## Onboarding a new app: the whole path

Prefer **"Onboarding with a manifest (recommended)"** above when the app only needs `POST /v1/messages`
and/or heartbeats — the app's own deploy then creates its destinations, routes and heartbeat checks from a
manifest in its repo, so the destinations/routes/heartbeats part of the "operator sets up" column below
moves to the app. The steps below, including the raw `curl` calls in "Copy-paste onboarding recipe", remain
the fallback (and are still required for the Access/token/identity/ntfy-publisher steps a manifest does
not cover).

Pick what the app needs, then follow the matching rows. Most apps want the first two.

| The app wants to…                                         | Use                      | The operator sets up                                                                 | Details                          |
| --------------------------------------------------------- | ------------------------ | ------------------------------------------------------------------------------------ | -------------------------------- |
| Push notifications straight to users' devices             | ntfy direct (Steps 1-5)  | A service token authorized on `notifs-api`                                           | "Integration in 5 steps"         |
| Send alerts and notifications with retries, a log, fan-out | `POST /v1/messages`      | A service token on the whole-host `notifs-delivery` app and the approval of the app's identity; destinations and routes come from the app's manifest (or the operator) | "Sending through delivery-api"   |
| Notify one person by email                                | `to.recipient`           | The above, plus the app's ntfy publisher (users are provisioned as in Step 2)        | "Recipients"                     |
| Send something later, or cancel / replace it              | `deliver_at`             | Nothing extra                                                                        | "Deferred send"                  |
| Know when a recurring job stops running or fails          | Heartbeats               | One check per job, declared in the app's manifest (or created by the operator)       | "Heartbeats"                     |

Migrating an app that already has its own senders? Follow **Migrating an existing app from its own
senders** below alongside these steps.

### Operator steps (once per app)

1. **Check the Access split first.** `notifs-delivery.4irl.app/v1/admin` must be its own path-scoped app
   (admin proxy token only) and the whole host a second app (proxy token plus per-app tokens). Never
   authorize an app token before that split exists. Details: "Operator runbook: exposing delivery-api and
   issuing app tokens".
2. **Create the app's service token(s).** `<app> → notifs-delivery`, added to the whole-host policy only
   (never the `/v1/admin` one), and `<app> → notifs-api` if the app provisions ntfy users. Hand over the
   Client ID and Client Secret; they are shown once.
3. **Approve the app's identity.** After the app's first deploy fails with "identity request #N awaits
   operator approval", open the Delivery tab, **App identities**, check that the requested repository,
   workflow file, ref and environment are the ones you expect, and approve. Full procedure, including
   reject, revoke and unbind: "Operator runbook: app identities".
4. **Destinations and routes.** Normally the app creates its own from its manifest. To make them yourself
   instead (operator-owned, usable by every app), add the destinations the app's messages should reach
   (ntfy, Discord or webhook) and the routes that send the app's traffic to them in the Delivery tab. Use the
   destination's send-test to confirm each one. Without a matching route or destination a send with no
   `to` fails with `422 no destination matched`.
5. **If the app sends to people (`to.recipient`):** mint the app's ntfy publisher (see "Operator runbook:
   ntfy publisher for an app").
6. **If the app has recurring jobs:** its manifest declares one heartbeat check per job (or create them
   yourself with the app's `app_id`, a `name`, a period, a grace of at least 1 hour for daily jobs, and
   its destinations).
7. **Verify the boundary.** The app's Access token gets `403` (from Access) on `/v1/admin/destinations`,
   and `401 app key required` on `POST /v1/messages` without the key; with the app's key the same call is
   `202`. Never run `make onboard cmd=verify` for an app manifest in production: it needs a key no
   operator command can supply there.

### App steps (once per app)

1. **Store the credentials as secrets** (`CF-Access-Client-Id`, `CF-Access-Client-Secret`), never in the
   repo. Then add the manifest and the `notifs-app` deploy step, deploy, and get the operator's approval
   (see "Onboarding from the app's own repo"); the deploy gives you the delivery key (`dak_...`), which you
   store on the app host as a file secret read through a `_FILE` variable.
2. **Send a first message and read the result.** `?wait=10` holds the response until each delivery has
   had a first attempt, so you can see `deliveries[].state` is `delivered`:

   ```bash
   curl -fsS -m 15 -X POST "https://notifs-delivery.4irl.app/v1/messages?wait=10" \
     -H "CF-Access-Client-Id: $NOTIFS_ID" -H "CF-Access-Client-Secret: $NOTIFS_SECRET" \
     -H "Authorization: Bearer $NOTIFS_KEY" \
     -H "Content-Type: application/json" \
     -d '{"app_id":"myapp","channel":"alerts","idempotency_key":"onboarding-test-1","payload":{},"display":{"title":"myapp is connected","body":"First message through delivery-api"}}'
   ```
3. **Make `idempotency_key` stable per event** (for example `<event>-<id>`), so a retry after a timeout
   can never double-send.
4. **For anything you cannot afford to lose, add the direct fallback** described under `critical`.
5. **Wire each recurring job to a heartbeat.** Send a success ping when the job succeeds and, where the
   job can tell, a `/fail` ping when it fails. Run the job once and confirm the check moves from
   **Waiting** to **Up** in the Heartbeats section.
6. **If you message people, link them to the shared setup page** instead of writing your own ntfy
   instructions.

Suggested naming (not enforced beyond the regexes in this guide): short purpose names for channels
(`alerts`, `reports`), job-shaped names for heartbeat checks (`nightly_backup`), and, when using
`to.recipient`, an `app_id` plus `channel` short enough that the whole topic stays within 64 characters.

### Who watches what

- **Your recurring jobs:** heartbeats. The job pushes a ping; silence past `period + grace` alerts.
- **The notification platform itself:** the watchdog, a Cloudflare Worker that probes delivery-api,
  provisioning-api and ntfy every 5 minutes from outside the VPS and posts changes to its own Discord
  channel.
- **Your app's own website or API:** not covered by either today. Keep using the app's own uptime
  monitoring for that.

### Pausing and removing an app

- **Planned maintenance:** pause the app's heartbeat checks (alerts stop, pings are still recorded) and
  resume them afterwards; the deadline restarts from the resume.
- **Removing an app:** unbind it in the admin UI (**App identities**, which stops every one of its keys
  at once; the objects it owned stay until you delete them), delete its heartbeat checks first (a
  destination that a check uses cannot be deleted), then remove its token from the Access policies, call `POST /v1/deprovision-app`, and delete
  its stored ntfy publisher (`DELETE /v1/admin/apps/<app>/ntfy-publisher`) so recipient submits fail
  closed.

---

## Migrating an existing app from its own senders

For an app that already posts to Discord, publishes to ntfy, or runs its own cron reporting. The goal is
to move it onto delivery-api **lane by lane**, with no big-bang cutover and a way back at every step. The
recipe is the same for every app.

### 1. Inventory (no code)

List every place the app sends or monitors, one row each:

| Trigger | Transport today | Secret it uses | Sync or fire-and-forget | Does the caller use the result? | Critical? | Schedule (if a job) |
| ------- | --------------- | -------------- | ----------------------- | ------------------------------- | --------- | ------------------- |

### 2. Map each row

| Today | Becomes |
| ----- | ------- |
| Post to a Discord webhook for an event | One envelope on a channel named for the purpose (`activity`, `contact`, `ops`), with `display.title` and `display.body`. The operator creates a Discord destination holding the **same webhook URL** (so the channel history continues) and a route from the app and channel to it. |
| The caller checks the HTTP result (for example a `204`) to set a flag | `?wait=10`. Delivered means `202` **and** every `deliveries[].state` is `delivered`. |
| Fire-and-forget thread or task | Same, ignoring the result; log a non-`202` status and the `message_id`. |
| "I ran" report from a cron or timer | A heartbeat: a success ping, plus `/fail` when the run knows it failed. |
| Failure message then recovery message, deduped with the app's own flags | If the condition is "the job did not run", a heartbeat does it with no flags (one down alert, one recovery). If the app detects a condition itself, keep its logic and send each transition as a message. |
| Delayed send built on a queue table or a cron sweep | `deliver_at`, plus cancel or replace by key. |
| Per-user push straight to ntfy | Keep it (supported), or `to.recipient` once the app's ntfy publisher exists. |
| An alert that must not be lost | `critical: true` with the direct fallback, kept for these alerts only. |
| Discord embeds, a custom username or avatar, mentions | **Not supported.** The Discord sink posts plain `**title**\nbody\nurl`, truncated to 2000 characters, with every mention suppressed. Keep such a message on its direct path if the extras matter. |

### 3. Put one adapter in the app

Route every call site through a single function, so migrating a lane is a config change and not a code hunt.
Suggested contract:

- Input: `channel`, `title`, `body`, an optional `url`, `priority` and `to`, a stable `idempotency_key`,
  `critical`, and `wait` (0 for fire-and-forget, up to 10 when the caller needs the outcome).
- Output: whether the service accepted the message, and whether it was delivered (only meaningful with
  `wait`). **It never raises into business logic.**
- Timeout of `wait` plus a few seconds. Retry only on a network error or a `5xx`, with the **same**
  `idempotency_key`.
- A mode per lane (suggested names): `direct` (the old path), `both` (old path plus the service, with the
  route pointed at a test channel), and `service`.
- Never log the Access token or the delivery key; log the status and `message_id`.

Python (standard library only):

```python
import json
import os
import urllib.error
import urllib.request

NOTIFS_URL = os.environ.get("NOTIFS_URL", "https://notifs-delivery.4irl.app")
NOTIFS_ID = os.environ.get("NOTIFS_ID", "")
NOTIFS_SECRET = os.environ.get("NOTIFS_SECRET", "")


def _notifs_key() -> str:
    """The app's delivery key (dak_...), read from the file the deploy wrote (the _FILE
    pattern) on every call, so a key replaced by a later deploy is picked up without a
    restart. A missing or unreadable file gives a blank key, which the service answers
    with a clean 401 (ok False) instead of an exception."""
    try:
        with open(os.environ["NOTIFS_KEY_FILE"], encoding="utf-8") as key_file:
            return key_file.read().strip()
    except (KeyError, OSError):
        return ""


def notify(
    *,
    app_id: str,
    channel: str,
    idempotency_key: str,
    title: str,
    body: str = "",
    url: str | None = None,
    priority: str | None = None,
    to: dict[str, str] | None = None,
    critical: bool = False,
    wait: int = 0,
) -> dict[str, object]:
    """Submit one message. Never raises; returns {ok, delivered, status, message_id}.

    ok: the service accepted the message (202). delivered: with wait > 0, every
    delivery reached `delivered` within the wait. Retry only when ok is False
    and status is None or >= 500, reusing the same idempotency_key.
    """
    display: dict[str, str] = {"title": title, "body": body}
    if url:
        display["url"] = url
    if priority:
        display["priority"] = priority
    envelope: dict[str, object] = {
        "app_id": app_id,
        "channel": channel,
        "idempotency_key": idempotency_key,
        "critical": critical,
        "payload": {},
        "display": display,
    }
    if to:
        envelope["to"] = to
    request = urllib.request.Request(
        f"{NOTIFS_URL}/v1/messages?wait={wait}",
        data=json.dumps(envelope).encode(),
        method="POST",
        headers={
            "Content-Type": "application/json",
            "CF-Access-Client-Id": NOTIFS_ID,
            "CF-Access-Client-Secret": NOTIFS_SECRET,
            "Authorization": f"Bearer {_notifs_key()}",
        },
    )
    try:
        with urllib.request.urlopen(request, timeout=wait + 5) as response:
            message = json.load(response)["message"]
    except urllib.error.HTTPError as error:
        return {"ok": False, "delivered": False, "status": error.code, "message_id": None}
    except (urllib.error.URLError, OSError, ValueError, KeyError):
        return {"ok": False, "delivered": False, "status": None, "message_id": None}
    states = [delivery["state"] for delivery in message["deliveries"]]
    return {
        "ok": True,
        "delivered": bool(states) and all(state == "delivered" for state in states),
        "status": 202,
        "message_id": message["message_id"],
    }


def heartbeat(*, app_id: str, name: str, failed: bool = False) -> bool:
    """Ping a heartbeat check (success, or /fail when failed). Never raises."""
    suffix = "/fail" if failed else ""
    request = urllib.request.Request(
        f"{NOTIFS_URL}/v1/heartbeats/{app_id}/{name}{suffix}",
        method="POST",
        headers={
            "CF-Access-Client-Id": NOTIFS_ID,
            "CF-Access-Client-Secret": NOTIFS_SECRET,
            "Authorization": f"Bearer {_notifs_key()}",
        },
    )
    try:
        with urllib.request.urlopen(request, timeout=10):
            return True
    except (urllib.error.URLError, OSError):
        return False
```

Shell (needs `curl` and `jq`; `jq` does the JSON escaping, which plain `printf` cannot do safely):

```bash
NOTIFS_URL="${NOTIFS_URL:-https://notifs-delivery.4irl.app}"

# The app's delivery key (dak_...), read from the file the deploy wrote on every call, so a key
# replaced by a later deploy is picked up. A missing or unreadable file prints nothing (a blank
# key, which the service answers with 401) instead of aborting the caller.
notifs_key() { cat "${NOTIFS_KEY_FILE:-/nonexistent}" 2>/dev/null || true; }

# notifs_notify <app_id> <channel> <idempotency_key> <title> [body]
# Prints the service's JSON response; exit 0 only when the service accepted the message (202).
notifs_notify() {
  local envelope
  envelope=$(jq -n --arg app "$1" --arg channel "$2" --arg key "$3" --arg title "$4" --arg body "${5:-}" \
    '{app_id:$app, channel:$channel, idempotency_key:$key, payload:{}, display:{title:$title, body:$body}}')
  curl -sS -m 15 -X POST "$NOTIFS_URL/v1/messages?wait=10" \
    -H "CF-Access-Client-Id: $NOTIFS_ID" -H "CF-Access-Client-Secret: $NOTIFS_SECRET" \
    -H "Authorization: Bearer $(notifs_key)" \
    -H "Content-Type: application/json" -d "$envelope" \
    -o /tmp/notifs_response.$$ -w '%{http_code}' | grep -q '^202$'
  local accepted=$?
  cat /tmp/notifs_response.$$ 2>/dev/null
  rm -f /tmp/notifs_response.$$
  return $accepted
}

# notifs_heartbeat <app_id> <name> [fail]   (pass the word "fail" to report a failed run)
notifs_heartbeat() {
  local suffix=""
  [ "${3:-}" = "fail" ] && suffix="/fail"
  curl -fsS -m 10 --retry 3 -X POST "$NOTIFS_URL/v1/heartbeats/$1/$2$suffix" \
    -H "CF-Access-Client-Id: $NOTIFS_ID" -H "CF-Access-Client-Secret: $NOTIFS_SECRET" \
    -H "Authorization: Bearer $(notifs_key)" > /dev/null
}
```

Both were run against a local stack (before delivery keys were required; the only change since is the
`Authorization: Bearer` header and reading the key file on every call): an accepted message, the same key resubmitted (same `message_id`), a
send with no route (`422`), a malformed envelope (`400`), an unreachable service (a clean failure, no
exception), and the heartbeat success, `/fail` and unknown-check cases. A workflow container that can only
run a restricted `curl` needs its allowlist to admit the delivery-api host and the two Access headers.

### 4. Roll out in steps, each reversible

0. **Operator setup** (see "Onboarding a new app"). Create destinations from the app's existing webhook
   URLs, but point the **first routes at a test channel**, so nothing reaches the real channel until you
   choose. Start on the app's staging environment with its own token (an `app_id` has exactly one approved
   workflow identity, so a staging deploy that runs from another workflow, ref or environment needs its
   own `app_id`).
1. **Heartbeats first.** They are purely additive: nothing is removed, and silent stalls of the app's jobs
   start alerting.
2. **Shadow one lane.** Run it in `both` mode with the route on the test channel, and compare messages
   against the old path for a few days.
3. **Switch lane by lane**, lowest risk first: background and ops messages, then activity pings, then any
   lane where a user waits on the result (use `wait`).
4. **Retire.** After a clean window, delete the old send code and its secret from compose and CI. Keep a
   single direct webhook only for `critical` alerts.

Rolling back any step is setting that lane's mode back to `direct`.

### 5. Cutover check, per lane

- A message reaches the intended channel, and the delivery log shows it as `delivered`.
- Resending the same `idempotency_key` returns `"duplicate": true` and does not post again.
- Disabling a destination makes a send addressed to it by name fail with `409 destination disabled`, and
  the app's direct fallback fires for `critical` messages.
- The app logs `message_id` for every send.
- Staging and local environments cannot post to the production channel: staging has its own token and
  test-channel routes, and local runs in a log-only mode.

### 6. Pitfalls

- **Idempotency keys:** stable per event and different for every event. A timestamp-based key defeats
  deduplication; a constant key drops every message after the first.
- **`202` is not delivery.** Only `wait` plus `deliveries[].state` tells you.
- **The webhook URL moves.** It becomes a sealed, masked destination secret. If it lived in many places,
  rotate it once the migration is done.
- **Egress.** The app's container must be allowed to reach `notifs-delivery.4irl.app`; allowlisting
  wrappers such as a restricted `curl` need updating.
- **Leaving both paths forever** doubles the failure modes. Set a date to retire the old one.
- **The delivery key rotates on every deploy.** The previous key works for 24 h, so reload the key (or
  re-read its file after a `401 invalid app key`) in long-running processes.

---

## Agent quick reference (delivery-api)

Everything a program or an LLM agent needs to call delivery-api correctly, in one place. It is derived
from the handlers and validators; the sections it points to hold the edge cases.

**Base URL:** `https://notifs-delivery.4irl.app`

**Auth, on every request:** headers `CF-Access-Client-Id: <id>` and `CF-Access-Client-Secret: <secret>`
(the network gate). An app's Access token reaches `/v1/messages*`, `/v1/heartbeats/*`, `/v1/apps/*`,
`/v1/app-identity/exchange`, `/readyz` and `/healthz`. `/v1/admin/*` needs the operator proxy token: an app
token gets a Cloudflare Access `403` before the API sees the request.

**App identity, on every app-facing call:** also send `Authorization: Bearer dak_...` (the app's delivery
key, see "App identity: how an app proves who it is") on `/v1/messages*`, `/v1/heartbeats/*` and
`/v1/apps/*`. Not needed on `/readyz`, `/healthz` or the exchange. The key's app is the caller: a missing or
malformed header is `401 app key required` (with `WWW-Authenticate: Bearer`), an unknown, revoked, unbound
or past-grace key is `401 invalid app key`, a present `app_id` (body, `?app_id=` or path) that differs from
the key's app is `403 app_id does not match app key`, and a failing identity store is `503 identity
unavailable` (it fails closed). Absent or empty `app_id` falls through to the endpoint's own validation.

**Format:** bodies are JSON (`Content-Type: application/json`); the heartbeat pings take no body. Errors
are `{"error":"<message>"}` with the HTTP status. Timestamps are RFC3339 UTC. `POST /v1/messages` rejects
unknown fields with `400 {"error":"invalid JSON body"}`. Destination secrets are masked on every read; the
only time a secret is returned is the new `signing_secret` from `rotate-secret`, once.

### App-facing endpoints (Access token plus delivery key)

Every row except the exchange and `/readyz` also returns the key errors above (`401`, `403`, `503 identity unavailable`).

| Method and path                                              | Request                       | Success                                                  | Errors                                                                          |
| ------------------------------------------------------------ | ----------------------------- | -------------------------------------------------------- | ------------------------------------------------------------------------------- |
| `POST /v1/app-identity/exchange` (Access token only, no key) | `{"app_id","token":"<GitHub OIDC JWT>"}` | `202 {"status":"pending","request_id"}` (awaits operator approval) or `200 {"status":"issued","app_id","key","key_id"}` (the plaintext key, once) | 400 malformed body or `app_id`, 401 `invalid identity token`, 403 `identity does not match the approved binding`, 429 `too many pending identity requests`, 503 `identity unavailable`. The `notifs-app` action calls this for you. |
| `POST /v1/messages` (optional `?wait=0..10`)                 | envelope JSON (see Envelope)  | `202 {"message":{…},"duplicate":bool,"replaced":bool}`   | 400 validation, 409 `destination disabled`, 413, 422 `no destination matched` / `app has no ntfy publisher`, 429, 503 `messages unavailable` |
| `GET /v1/messages/{id}?app_id=<app>`                         | none                          | `200 {"message":{…}}`                                    | 400 `app_id is required`, 404 `message not found`                               |
| `GET /v1/messages?app_id=<app>&idempotency_key=<key>`        | none                          | `200 {"message":{…}}`                                    | 400, 404                                                                        |
| `DELETE /v1/messages/{id}?app_id=<app>` or `DELETE /v1/messages?app_id=<app>&idempotency_key=<key>` | none | `200 {"message_id","cancelled":bool,"status"}` (idempotent) | 400                                          |
| `POST /v1/heartbeats/{app_id}/{name}`                        | none                          | `200 {"app_id","name","state"}`                          | 400, 404 `check not found`, 503 `heartbeats unavailable`                        |
| `POST /v1/heartbeats/{app_id}/{name}/fail`                   | none                          | `200 {"app_id","name","state"}` (state `down`)           | same                                                                            |
| `GET /readyz`                                                | none                          | `200` readiness body (below)                             | `503 {"status":"unavailable"}` (database unreachable) or `503` with `"status":"schema_mismatch"` |

**App-managed destinations (app key).** An app manages its own destinations under
`/v1/apps/{app_id}/destinations`, with `Authorization: Bearer dak_...` whose app is the `{app_id}` in the path
(another app's id is `403 app_id does not match app key`). The bodies, masking and rotate response are the
admin ones; the owner is the calling app and is never sent in the body (an `owner_*` or `app_id` field is
`400 invalid JSON body`).

| Method and path                                                         | Request body                                            | Success                                         | Errors                                                                 |
| ----------------------------------------------------------------------- | ------------------------------------------------------- | ----------------------------------------------- | ---------------------------------------------------------------------- |
| `POST /v1/apps/{app_id}/destinations`                                   | `{"name","type","enabled"?,"config":{…},"secrets":{…}}` | `201 {"destination":{…}}`                       | 400 validation, 409 `destination already exists`, 401 `app key required`/`invalid app key`, 403 `app_id does not match app key`, 503 `registry unavailable`/`identity unavailable`/`destination url check unavailable` |
| `GET /v1/apps/{app_id}/destinations`                                    | none                                                    | `200 {"destinations":[…]}` (only the app's own) | 401 `app key required`/`invalid app key`, 403 `app_id does not match app key`, 503 `registry unavailable`/`identity unavailable` |
| `GET`, `PATCH`, `DELETE /v1/apps/{app_id}/destinations/{name}`          | `PATCH`: `{"enabled"?,"config"?,"secrets"?}` per key    | `200` (`DELETE`: `{"name","deleted"}`)          | `PATCH` 400 validation; 404 `destination does not exist`; `DELETE` also 409 `destination is in use`; 401 `app key required`/`invalid app key`, 403 `app_id does not match app key`, 503 `registry unavailable`/`identity unavailable` (`PATCH` also `destination url check unavailable`) |
| `POST /v1/apps/{app_id}/destinations/{name}/rotate-secret`              | `{"overlap_hours":1..168}` (webhook only)               | `200` destination plus the new `signing_secret` | 400, 404, 422, 401 `app key required`/`invalid app key`, 403 `app_id does not match app key`, 503 `registry unavailable`/`identity unavailable`/`destination url check unavailable` |

- A destination the app does not own (the operator's or another app's) is `404`, the same bytes as a name
  that does not exist, on every method. `DELETE` of an absent or foreign name is therefore `404`, not the
  idempotent `200` of the admin API.
- `name` must start with `<app_id>_` (`400 name must start with <app_id>_`) and an ntfy `config.topic` with
  `<app_id>-` (`400 config.topic must start with <app_id>-`). Each app uses its own destinations and the
  operator's; another app's destination is never usable.
- **Prefix overlap and name collisions.** The prefix rule does not tell an app id from another app id that
  extends it: app `app` may create `app_a_hook`, a name inside the `<app_id>_` space of app `app_a`. The row
  stays owned by `app` and is never usable by `app_a`, but `app_a`'s own create of that name answers `409
  destination already exists` until the operator deletes the squatting destination through the admin API. A
  `409 destination already exists` on create also tells the caller that the name is occupied by anyone (the
  operator, another app, or itself); a `GET` of a name it does not own stays `404`.
- **Saved URLs are checked when you register them.** The `webhook` `secrets.url` and the `discord`
  `secrets.webhook_url` are resolved on create, on a PATCH that changes the URL, and again on every
  `rotate-secret`; every address must be public, else `400 secrets.url must resolve to public addresses`
  (a host that does not resolve is `400 secrets.url host could not be resolved`). Use a public HTTPS URL
  whose DNS answers from the public internet. The check does not replace the one made on every delivery
  attempt, which still refuses a host that later resolves to a private address.
- **`rotate-secret` re-resolves the stored URL.** A transient DNS failure of the app's own host therefore
  answers `400 secrets.url host could not be resolved` until DNS recovers; retry the rotation later.

**App-managed routes (app key).** An app manages its own routes under `/v1/apps/{app_id}/routes`, with the same
`Authorization: Bearer dak_...` rules as the destinations above. The bodies and the `route` object are the
admin ones; the owner is the calling app and is never sent (an `owner_*` or `app_id` field is `400 invalid
JSON body`).

| Method and path                                      | Request body                                                 | Success                                      | Errors                                                                 |
| ---------------------------------------------------- | ------------------------------------------------------------ | -------------------------------------------- | ---------------------------------------------------------------------- |
| `POST /v1/apps/{app_id}/routes`                      | `{"app_pattern"?,"channel_pattern","destinations":[…],"enabled"?}` | `201 {"route":{…}}`                    | 400 validation, 400 `app_pattern must be the app's own id`, 400 `unknown destination`, 409 `route already exists`, 401/403 (key), 503 `registry unavailable`/`identity unavailable` |
| `GET /v1/apps/{app_id}/routes`                       | none                                                         | `200 {"routes":[…]}` (only the app's own)    | 401/403 (key), 503                                                     |
| `GET`, `PATCH`, `DELETE /v1/apps/{app_id}/routes/{id}` | `PATCH`: any of `app_pattern`, `channel_pattern`, `destinations`, `enabled` | `200` (`DELETE`: `{"id","deleted"}`) | 400 invalid id; `PATCH` 400 as for `POST`, 409; 404 `route does not exist`; 401/403 (key), 503 |

- A route's `app_pattern` is always your own app id. Omit it on `POST` (it defaults to the app id) or send your
  own id; a wildcard (`*`), a glob or another app's id is `400 app_pattern must be the app's own id`, on `PATCH`
  too. A route you create therefore matches only your own messages.
- A route may link your own destinations and the operator's. Another app's destination is `400 unknown
  destination` (the same answer as a name that does not exist).
- A route you do not own (the operator's, a wildcard route, another app's) is `404 route does not exist`, the
  same bytes as an id that does not exist, on every method. `DELETE` of an absent or foreign id is therefore
  `404`, not the idempotent `200` of the admin API.
- A `409 route already exists` means the pair (your app id, `channel_pattern`) is taken by any route, yours or
  an operator route for your app id; a `GET` of an id you do not own stays `404`.
- The `route` objects you receive carry `"owner_kind":"app"` and `"owner_app_id":"<app_id>"`.
- Other errors: `400 invalid route id` (an `{id}` that is not a positive decimal integer), `400 request violates
  a data constraint` (a database CHECK the validators did not catch, mapped defensively) and `413 request body
  too large`.

**App-managed heartbeat checks (app key).** An app manages its own checks under `/v1/apps/{app_id}/heartbeats`,
with the same key rules. The bodies and the `check` object are the admin ones. A check you create is
registered under your own app id, so you ping it at `POST /v1/heartbeats/{app_id}/{name}` with the same key.

| Method and path                                              | Request body                                                       | Success                                      | Errors                                                                 |
| ------------------------------------------------------------ | ------------------------------------------------------------------ | -------------------------------------------- | ---------------------------------------------------------------------- |
| `POST /v1/apps/{app_id}/heartbeats`                          | `{"app_id"?,"name","period_seconds","grace_seconds","destinations":[…]}` | `201 {"check":{…}}`                    | 400 validation, 400 `app_id must be the app's own id`, 400 `unknown destination`, 409 `check already exists`, 401/403 (key), 503 `heartbeats unavailable`/`identity unavailable` |
| `GET /v1/apps/{app_id}/heartbeats`                           | none                                                               | `200 {"checks":[…]}` (only the app's own)    | 401/403 (key), 503                                                     |
| `GET`, `PATCH`, `DELETE /v1/apps/{app_id}/heartbeats/{id}`   | `PATCH`: any of `period_seconds`, `grace_seconds`, `destinations`  | `200` (`DELETE`: `{"id","deleted"}`)         | 400 invalid id; `PATCH` 400 validation / `app_id and name are immutable`; 404 `check not found`; 401/403 (key), 503 |
| `POST /v1/apps/{app_id}/heartbeats/{id}/pause` and `/resume` | none                                                               | `200 {"check":{…}}`                          | 400 invalid id, 404 `check not found`, 401/403 (key), 503              |

- A body `app_id` may be omitted, empty, `null` or your own id, on `POST` and `PATCH` alike; any other value is
  `400 app_id must be the app's own id`. A check's `name` cannot be changed. An `owner_kind` or `owner_app_id`
  field in a check body is `400 invalid JSON body`.
- The `check` objects you receive carry `"owner_kind":"app"` and `"owner_app_id":"<app_id>"`.
- Other errors: `400 invalid check id` (an `{id}` that is not a positive decimal integer), `400 request violates
  a data constraint` (a database CHECK the validators did not catch, mapped defensively) and `413 request body
  too large`.
- A check may alert your own destinations and the operator's. Another app's destination is `400 unknown
  destination`.
- A check you do not own (the operator's, even one registered under your `app_id`, or another app's) is `404
  check not found`, the same bytes as an id that does not exist, on every method including `pause`, `resume`
  and an empty `PATCH`. `DELETE` of an absent or foreign id is `404`, not the idempotent `200` of the admin API.
- A `409 check already exists` means the pair (your app id, `name`) is taken by any check, yours or an operator
  check registered under your app id; a `GET` of an id you do not own stays `404`.

Message object (illustrative values; `status` is `pending`, `delivered`, `dead`, `partial`, `scheduled` or
`cancelled`; a delivery `state` is `pending`, `delivered`, `dead`, `scheduled` or `cancelled`):

```json
{
  "message": {
    "message_id": "<uuid>",
    "app_id": "myapp",
    "channel": "alerts",
    "idempotency_key": "onboarding-test-1",
    "critical": false,
    "status": "delivered",
    "deliver_at": null,
    "created_at": "2026-10-21T04:00:00Z",
    "cancelled_at": null,
    "deliveries": [
      {
        "id": 1,
        "destination": "ops_discord",
        "state": "delivered",
        "dead_reason": null,
        "attempt_count": 1,
        "last_status_code": 204,
        "next_attempt_at": null,
        "delivered_at": "2026-10-21T04:00:01Z"
      }
    ]
  },
  "duplicate": false,
  "replaced": false
}
```

Readiness body: `{"status":"ok","schema_version":8,"expected_schema_version":8,"last_migration_failure":null,"heartbeat_checker":{"last_tick_at":"<RFC3339>","stale":false},"heartbeat_prober":{"last_tick_at":"<RFC3339>","stale":false}}`.
The HTTP status stays `200` even when `heartbeat_checker.stale` or `heartbeat_prober.stale` is `true`; read the fields.

### Operator endpoints (proxy token; used to onboard an app)

| Method and path                                      | Request body                                                                                  | Success                                          | Errors                                                                       |
| ---------------------------------------------------- | --------------------------------------------------------------------------------------------- | ------------------------------------------------ | ---------------------------------------------------------------------------- |
| `POST /v1/admin/destinations`                        | `{"name","type":"ntfy"\|"discord"\|"webhook","enabled"?,"config":{…},"secrets":{…}}`         | `201 {"destination":{…}}`                        | 400 validation, 409 `destination already exists`                             |
| `GET /v1/admin/destinations`                         | none                                                                                          | `200 {"destinations":[…]}`                       |                                                                              |
| `GET`, `PATCH`, `DELETE /v1/admin/destinations/{name}` | `PATCH`: `{"enabled"?,"config"?,"secrets"?}` merged per key                                | `200` (`DELETE`: `{"name","deleted"}`)           | 404 `destination does not exist`; `DELETE` also 409 `destination is in use` (a route or a heartbeat check uses it) |
| `POST /v1/admin/destinations/{name}/rotate-secret`   | `{"overlap_hours":1..168}` (webhook destinations only)                                        | `200` destination plus the new `signing_secret`  | 400, 404, 422 `destination type does not support rotation`                   |
| `POST /v1/admin/routes`                              | `{"app_pattern","channel_pattern","destinations":["<name>",…],"enabled"?}`                    | `201 {"route":{…}}`                              | 400 validation or `unknown destination`, 409 `route already exists`          |
| `GET /v1/admin/routes`; `GET`, `PATCH`, `DELETE /v1/admin/routes/{id}` | `PATCH`: any of the four fields; `destinations` replaces the whole set       | `200` (`DELETE`: `{"id","deleted"}`)             | 404 `route does not exist`; `PATCH` of an app-owned route's `app_pattern` away from its owner is 400 `request violates a data constraint` |
| `POST /v1/admin/heartbeats`                          | `{"app_id","name","period_seconds","grace_seconds","destinations":["<name>",…]}`              | `201 {"check":{…}}`                              | 400 validation or `unknown destination`, 409 `check already exists`          |
| `GET /v1/admin/heartbeats`                           | none                                                                                          | `200 {"checks":[…]}`                             |                                                                              |
| `GET`, `PATCH`, `DELETE /v1/admin/heartbeats/{id}`   | `PATCH`: any of `period_seconds`, `grace_seconds`, `destinations`                             | `200 {"check":{…}}` (`DELETE`: `{"id","deleted"}`, idempotent) | 400 `app_id and name are immutable`, 404 `check not found`         |
| `POST /v1/admin/heartbeats/{id}/pause` and `/resume` | none                                                                                          | `200 {"check":{…}}` (idempotent)                 | 404                                                                          |
| `POST /v1/admin/apps/{app_id}/ntfy-publisher`        | none                                                                                          | `200 {"publisher":{"app_id","publisher_user_id","topic_pattern","created_at","updated_at"}}` (never the token) | 502 mint failed, 503 not configured |
| `GET`, `DELETE /v1/admin/apps/{app_id}/ntfy-publisher` | none                                                                                        | `200` as above / `204` (idempotent)              | 404 `GET` when none is stored                                                |
| `POST /v1/admin/messages`                            | the `POST /v1/messages` envelope, any `app_id` (optional `?wait=0..10`)                       | `202`, same as `POST /v1/messages`               | as `POST /v1/messages`, no key needed (admin send-test uses it)              |
| `GET /v1/admin/app-identity/requests`                | none                                                                                          | `200 {"requests":[…]}` (pending first, then rejected from the last 30 days) |                                                  |
| `POST /v1/admin/app-identity/requests/{id}/approve`  | none                                                                                          | `201 {"binding":{…}}` (the request is deleted)   | 400 `invalid request id`, 404 `request does not exist`, 409 `request is not pending` / `app already bound` / `workflow identity already bound` |
| `POST /v1/admin/app-identity/requests/{id}/reject`   | none                                                                                          | `200 {"request":{…,"status":"rejected"}}` (idempotent) | 400, 404                                                              |
| `GET /v1/admin/app-identity/bindings`                | none                                                                                          | `200 {"bindings":[{app_id, github_repository, …, "keys":[{id, key_hint, state, …}]}]}`; `state` is `active`, `superseded` or `revoked` |  |
| `POST /v1/admin/app-identity/keys/{id}/revoke`       | none                                                                                          | `200 {"key":{…}}` (idempotent)                   | 400 `invalid key id`, 404 `key does not exist`                               |
| `DELETE /v1/admin/app-identity/bindings/{app_id}`    | none                                                                                          | `200 {"app_id","deleted":bool}` (idempotent; every key stops, owned objects stay) | 400 `invalid app_id`                      |

**Destination `config` and `secrets` by `type`** (an unknown key is `400 unknown field <section>.<key>`):

| `type`    | `config`                                  | `secrets`                                                                                          |
| --------- | ----------------------------------------- | -------------------------------------------------------------------------------------------------- |
| `ntfy`    | `topic` (required, `[A-Za-z0-9_-]`, 1-64) | `access_token` (optional, `tk_` plus 29 lowercase letters or digits)                               |
| `discord` | none                                      | `webhook_url` (required, https, a Discord host, path `/api/webhooks/<id>/<token>`)                 |
| `webhook` | none (rotation adds `previous_secret_expires_at`) | `url` (required, https) and `signing_secret` (required, printable ASCII, 32-256 bytes)     |

A destination view is `{"name","type","enabled","disabled_reason","config","secrets"(masked),"created_at","updated_at"}`.
`enabled` defaults to `true` on create.

**Route patterns:** `app_pattern` is `*` or one exact `app_id`; `channel_pattern` is `*`, one exact channel,
or a prefix ending in one `*` (for example `alert_*`). A route lists 1-20 distinct destination names. A
route view is `{"id","app_pattern","channel_pattern","destinations","enabled","created_at","updated_at"}`.

**Heartbeat bounds:** `app_id` and `name` match `^[a-z0-9][a-z0-9_]{0,62}$`; `period_seconds` 60 to
2678400; `grace_seconds` 60 to 604800; 1-20 distinct, existing `destinations`. A check view is
`{"id","app_id","name","period_seconds","grace_seconds","paused","state","last_ping_at","last_ping_kind","down_since","deadline_at","destinations":[{"name","type","enabled"}],"created_at","updated_at"}`
with `state` one of `new`, `up`, `late`, `down` and `last_ping_kind` `success` or `fail`.

### Copy-paste onboarding recipe

Prefer **"Onboarding with a manifest (recommended)"** above (`make onboard`) for steps 1-3; the raw
`curl` calls below are the fallback for a one-off change or for debugging what the tool would send.

Variables: `OP_ID` / `OP_SECRET` (operator proxy token), `NOTIFS_ID` / `NOTIFS_SECRET` (the app's Access
token), `NOTIFS_KEY` (the app's delivery key `dak_...`, obtained as described in "Onboarding from the
app's own repo"), `B=https://notifs-delivery.4irl.app`. Names below are examples.

```bash
# 1-3 (operator): a destination, a route to it, and a heartbeat check
curl -fsS -X POST "$B/v1/admin/destinations" -H "CF-Access-Client-Id: $OP_ID" -H "CF-Access-Client-Secret: $OP_SECRET" -H "Content-Type: application/json" \
  -d '{"name":"ops_discord","type":"discord","secrets":{"webhook_url":"https://discord.com/api/webhooks/<id>/<token>"}}'
curl -fsS -X POST "$B/v1/admin/routes" -H "CF-Access-Client-Id: $OP_ID" -H "CF-Access-Client-Secret: $OP_SECRET" -H "Content-Type: application/json" \
  -d '{"app_pattern":"myapp","channel_pattern":"alerts","destinations":["ops_discord"]}'
curl -fsS -X POST "$B/v1/admin/heartbeats" -H "CF-Access-Client-Id: $OP_ID" -H "CF-Access-Client-Secret: $OP_SECRET" -H "Content-Type: application/json" \
  -d '{"app_id":"myapp","name":"nightly_backup","period_seconds":86400,"grace_seconds":3600,"destinations":["ops_discord"]}'

# 4 (app): send a message and read the delivery result
curl -fsS -X POST "$B/v1/messages?wait=10" -H "CF-Access-Client-Id: $NOTIFS_ID" -H "CF-Access-Client-Secret: $NOTIFS_SECRET" -H "Authorization: Bearer $NOTIFS_KEY" -H "Content-Type: application/json" \
  -d '{"app_id":"myapp","channel":"alerts","idempotency_key":"onboarding-test-1","payload":{},"display":{"title":"myapp is connected","body":"First message"}}'

# 5 (app): ping the heartbeat when the job succeeds (append /fail when it fails)
curl -fsS -m 10 --retry 3 -X POST "$B/v1/heartbeats/myapp/nightly_backup" -H "CF-Access-Client-Id: $NOTIFS_ID" -H "CF-Access-Client-Secret: $NOTIFS_SECRET" -H "Authorization: Bearer $NOTIFS_KEY"
```

Success for step 4 is `202` with every `deliveries[].state` equal to `delivered`; for step 5 it is `200`
with `"state":"up"`.

### Retry rules for a program or agent

- **Safe to retry with the same body:** a network error, a timeout, any `5xx`, and `503`. Keep the same
  `idempotency_key`; a duplicate comes back flagged `"duplicate": true` and never sends twice.
- **Do not retry unchanged:** `400`, `404`, `409`, `413`, `422` (fix the request or the operator setup
  first), and `429` (a scheduled-message cap, not a transient error).
- **A `202` is not delivery.** Without `?wait`, or when a delivery is still `pending`, read
  `GET /v1/messages/{id}?app_id=` until each `deliveries[].state` is final (`delivered` or `dead`).
- **A `dead` delivery is a destination problem, not a reason to resend.** `dead_reason` and
  `last_status_code` say why (for example `terminal_status` with `403`: the destination rejected the
  request). Fix the destination, then ask the operator to replay the delivery; re-posting the same
  `idempotency_key` returns the stored message and does not resend.
- **A `403` without a JSON `error` body, or a redirect to a Cloudflare login page,** comes from Access,
  not the API: the token is missing, wrong, or not authorized on that path.
- **A `401` with `app key required` or `invalid app key`** is the delivery key: the header is missing or
  malformed, or the key is unknown, revoked, unbound or more than 24 h past being superseded. Re-read the
  key the latest deploy stored; do not retry unchanged. **A `403 app_id does not match app key`** means the
  request names another app than the key's.

---

## Mental model

The notification service is two hosts:

| Host                          | What it is                                                            | How you authenticate to it                            |
| ----------------------------- | --------------------------------------------------------------------- | ----------------------------------------------------- |
| `https://notifs-api.4irl.app` | **Provisioning API** — creates users/tokens/ACLs.                     | Cloudflare Access **service token** (your app's own). |
| `https://notifs.4irl.app`     | **ntfy server** — the actual pub/sub. You publish and subscribe here. | An **ntfy access token** (`Authorization: Bearer …`). |

You provision against the **provisioning API**, then publish/subscribe against **ntfy**.

**Identity is the user's email.** The service hashes it into a stable, opaque `person_hash`
(16 chars). The same email is the same person across every 4IRL app — you never send or store the
raw email in a topic. You do **not** compute the hash yourself; the API returns it.

**There are two kinds of token, doing two different jobs:**

| Token                | Minted by                               | Scope                                   | Lives where                                           | Used to                   |
| -------------------- | --------------------------------------- | --------------------------------------- | ----------------------------------------------------- | ------------------------- |
| **Publisher token**  | `POST /v1/provision-app` (once per app) | write-only on `{app_id}-*`              | your app's **backend** secret store                   | **publish** notifications |
| **Subscriber token** | `POST /v1/provision` (once per user)    | read-only on `{app_id}-{person_hash}-*` | the user's **client** (or your backend, if you relay) | **receive** notifications |

**Topic naming** (one channel per app: `all`):

```
{app_id}-{person_hash}-all           ← one channel per app; use this one
{app_id}-{person_hash}-*             ← everything for one user (their subscriber token can read this)
{app_id}-*                           ← everything for your app (your publisher token can write this)
```

Example: `urls4irl-v4sf4e5teivpe3zi-all`.

> **Tokens are shown once.** Every mint endpoint reveals the token exactly once — capture and store
> it at that moment. There is no "fetch my token again" endpoint. If lost, mint a new one.

---

## Prerequisites (one-time, operator action)

Ask the notifs operator to give your app a **Cloudflare Access service token** and authorize it:

1. Zero Trust → Access → Service Auth → create a token named e.g. `urls4irl → notifs-api`.
2. Add that token to the **Service Auth policy** on the `notifs-api.4irl.app` Access application.
3. Hand you the **Client ID** and **Client Secret** (shown once).

Your backend stores these two values as secrets and sends them on **every** provisioning-API call:

```
CF-Access-Client-Id: <client-id>
CF-Access-Client-Secret: <client-secret>
```

Without this, `notifs-api.4irl.app` returns a Cloudflare Access `403`/redirect — you never reach the API.

`app_id` rule: lowercase letters, digits, and underscores, 1–63 chars (e.g. `urls4irl`, `tasktracker`).

This token is for `notifs-api` only. An app that also sends through delivery-api or pings heartbeats
needs a second token authorized on `notifs-delivery`; see **Onboarding a new app** above.

---

## Integration in 5 steps

### Step 1 — Mint your publisher token (once per app)

```
POST https://notifs-api.4irl.app/v1/provision-app
Content-Type: application/json
CF-Access-Client-Id: <id>
CF-Access-Client-Secret: <secret>

{ "app_id": "urls4irl" }
```

Response:

```json
{
  "app_id": "urls4irl",
  "publisher_user_id": "urls4irl-publisher",
  "topic_pattern": "urls4irl-*",
  "token": "tk_<publisher-token>"
}
```

**Store `token`** in your backend secret store. This is your write credential for all
`urls4irl-*` topics. (Re-calling this endpoint mints an *additional* publisher token; it does
**not** revoke the old one — rotation is by-issuing. To force-revoke existing tokens instead — e.g.
after a leak — pass `{ "app_id": "urls4irl", "rotate": true }`; see the API reference note on `rotate`.)

### Step 2 — Provision a user (once per user, when they enable notifications)

```
POST https://notifs-api.4irl.app/v1/provision
Content-Type: application/json
CF-Access-Client-Id: <id>
CF-Access-Client-Secret: <secret>

{ "app_id": "urls4irl", "email": "alice@example.com" }
```

Response:

```json
{
  "user_id": "u_v4sf4e5teivpe3zi",
  "app_id": "urls4irl",
  "person_hash": "v4sf4e5teivpe3zi",
  "topic_pattern": "urls4irl-v4sf4e5teivpe3zi-*",
  "broadcast_topic": "urls4irl-broadcast",
  "token": "tk_<subscriber-token>",
  "password": "k7mq-x2pw-9rnd-ft4h"
}
```

`password` is optional: it is present only when this call generated the person's ntfy password. **Always
display it** (once; never store or log it) and see "Subscriber login — the generated password".

**Store, keyed to your user:**
- `person_hash` — you need it to build the topic you publish to. **Always store this.**
- `token` (subscriber token) — store it if your backend relays notifications; otherwise deliver it
  to the user's client (see Step 4). Either way, capture it now — it's shown once.

> Re-provisioning the *same* user resets their tokens (issues a fresh one and invalidates the old).
> Provision once per user and persist the result; don't call it on every login.

### Step 3 — Publish a notification (ntfy)

Publish to the user's `all` channel with your **publisher token**:

```
POST https://notifs.4irl.app/urls4irl-v4sf4e5teivpe3zi-all
Authorization: Bearer tk_<publisher-token>
Title: New shared URL
Priority: default

Alice added a link to your UTub.
```

The topic is `{app_id}-{person_hash}-all`. **Always publish to `-all`**: the ntfy iOS/Android apps
have no wildcard subscribe, so every extra channel is another manual subscription for the user.
Distinguish notification types with the `Title`, `Tags` and `Priority` headers instead, and **always
set `Title`**, or ntfy shows the raw topic as the notification title. Use a second channel only by
explicit need. Your publisher token can write any `urls4irl-*` topic, so no per-user publish
credential is needed. (This is the standard ntfy publish API — see ntfy docs for `Title`,
`Priority`, `Tags`, `Click`, attachments, etc.)

### Step 4 — Receive notifications (ntfy, on the user's side)

The user's client subscribes with the **subscriber token** (read-only on their `*` topics):

```
GET https://notifs.4irl.app/urls4irl-v4sf4e5teivpe3zi-all/json
Authorization: Bearer tk_<subscriber-token>
```

ntfy supports `/json` (streaming JSON), `/sse`, and WebSocket subscriptions, plus the ntfy
mobile/desktop apps. Two delivery models:
- **Client-direct (recommended for this deployment):** hand the subscriber token to the user's
  browser/app; it subscribes to ntfy directly. Simplest; the token lives on the client.
- **Backend-relay:** your backend holds the subscriber token, subscribes, and forwards to the user
  over your own channel (WebSocket/SSE/web-push). Keeps the token server-side — use only when a token
  must never reach the client (your backend then owns reconnection/offline delivery that ntfy would
  otherwise handle).

> **iOS clients need one extra step:** the ntfy iOS app's **Default server** must be set to exactly
> `https://notifs.4irl.app`, or notifications silently never arrive. Link iOS users to the shared setup
> page (see **Link your users to the setup page** below) rather than documenting this yourself.

### Step 5 — Deprovision (when the user disables notifications or leaves)

```
POST https://notifs-api.4irl.app/v1/deprovision
Content-Type: application/json
CF-Access-Client-Id: <id>
CF-Access-Client-Secret: <secret>

{ "app_id": "urls4irl", "email": "alice@example.com" }
```

Response: `{ "user_id": "u_v4sf4e5teivpe3zi", "app_id": "urls4irl", "removed": true }`.

This removes *your app's* topic access for that user; the ntfy user is deleted entirely once they
have no remaining topic access from any app. (You may pass `"user_id": "u_…"` instead of `"email"`.)

---

## Broadcast / site-wide announcements

Sometimes you want to reach **all** of your app's subscribers at once — a maintenance window, an outage
notice, a site-wide announcement — rather than one user's personal channel. Each app has a single shared
**broadcast topic** for exactly this: `{app_id}-broadcast` (e.g. `urls4irl-broadcast`).

**Publish** to it with your **publisher token** — the write-only `{app_id}-*` credential from Step 1
already covers `{app_id}-broadcast`, so there is **no** extra token to mint:

```
POST https://notifs.4irl.app/urls4irl-broadcast
Authorization: Bearer tk_<publisher-token>
Title: Scheduled maintenance
Priority: high

urls4irl will be down for ~10 min at 02:00 UTC for maintenance.
```

**⚠️ Admins only — restrict who can trigger a broadcast.** A broadcast reaches **every** subscriber of
your app at once, so treat the send path as a privileged action. `notifs.4irl.app` authenticates your
app-level **publisher token** — it has no knowledge of your app's individual users or their roles, so it
**cannot** distinguish an admin from any other user; to ntfy, the token *is* the app. Enforcing
admin-only is therefore **your app's responsibility**: keep the publisher token in your backend (Step 1 —
never ship it to a client) and gate the broadcast-trigger endpoint behind your own admin authorization
check. The token itself is a strong random credential (32 chars, `crypto/rand` — not guessable), so the
real risks are a **leaked** token or a **send path reachable by non-admins**, both of which only your app
can prevent. (A personal-channel publish only ever hits one user; a broadcast hits everyone, so it
warrants the stricter gate.)

**Receive** it on the client by also subscribing to `{app_id}-broadcast`, exactly like a personal topic
(Step 4) but with the shared name:

```
GET https://notifs.4irl.app/urls4irl-broadcast/json
Authorization: Bearer tk_<subscriber-token>
```

Every provisioned user is granted **read** on `{app_id}-broadcast` automatically at `POST /v1/provision`
— it is always-on per app (no per-user opt-in/opt-out), so **no extra registration** is needed. The
topic name is also returned as `broadcast_topic` in the `/v1/provision` response (see Step 2), so your
client can discover it instead of hardcoding. The grant is removed when the user is deprovisioned, so a
departed user stops receiving broadcasts.

---

## Sending through delivery-api (`POST /v1/messages`)

Besides publishing to ntfy directly, an app can submit an envelope to delivery-api and let it deliver
(retries, backoff and a delivery log included). The human-facing sinks and per-person recipients below
apply to that path.

delivery-api is reachable at `https://notifs-delivery.4irl.app`, behind Cloudflare Access. Send the same
`CF-Access-Client-Id` / `CF-Access-Client-Secret` headers as for `notifs-api`, using a service token the
operator has authorized on the `notifs-delivery` Access application (see the operator runbook below). Your
app token reaches `/v1/messages`, `/v1/heartbeats/*` (see Heartbeats below), `/v1/apps/*`, `/readyz` and
`/healthz`; `/v1/admin/*` returns 403 to it.

Every `/v1/messages` call, and every heartbeat ping, must also carry your app's delivery key as
`Authorization: Bearer dak_...` (see "App identity: how an app proves who it is"). The key, not the
`app_id` you write, says who the caller is: you can submit as, read, cancel or replace only your own app's
messages, and ping or fail only your own app's checks. A request whose `app_id` (body, `?app_id=` or path)
names another app is `403 app_id does not match app key`; without a valid key it is `401`. The Access
token alone is no longer enough.

### Envelope contract

`POST /v1/messages` takes one JSON object. An unknown field, a wrong JSON type or trailing data is
`400 {"error":"invalid JSON body"}`. The request body is capped at 256 KiB.

| Field             | Required | Rule                                                                                       |
| ----------------- | -------- | ------------------------------------------------------------------------------------------ |
| `app_id`          | yes      | `^[a-z0-9][a-z0-9_]{0,62}$`                                                                |
| `channel`         | yes      | `^[a-z0-9][a-z0-9_]{0,31}$` (exact name, no wildcards)                                     |
| `idempotency_key` | yes      | 1-200 printable, non-space ASCII characters                                                |
| `to`              | no       | `{"destination": "<name>"}` or `{"recipient": "<email or person_hash>"}`, never both; empty fans out through the routes |
| `critical`        | no       | boolean, recorded only (see `critical` below)                                              |
| `payload`         | yes      | a single JSON object, at most 128 KiB, storable in Postgres jsonb                         |
| `display`         | no       | `title` (max 250 bytes), `body` (max 4096), `url` (max 2048, https), `priority` (`min` `low` `default` `high` `urgent`) |
| `deliver_at`      | no       | RFC3339 timestamp; a future time holds the message until then (see Deferred send below); absent, `null` or a past time sends at once |

Validation is checked in a fixed order (`app_id`, `channel`, `idempotency_key`, `to`, `deliver_at`,
`payload`, `display`) and the first failure is returned as `400 {"error":"<message>"}`, for example
`invalid app_id`, `invalid channel`, `invalid idempotency_key`, `invalid to.destination`,
`to.destination and to.recipient cannot both be set`, `to.recipient must be an email or person_hash`,
`invalid deliver_at`, `invalid payload`, `invalid display.title`, `invalid display.body`,
`invalid display.url`, `invalid display.priority`.

A successful submit is `202 {"message": {...}, "duplicate": false, "replaced": false}`. The message
carries `message_id`, `app_id`, `channel`, `idempotency_key`, `critical`, `status` (`pending`,
`delivered`, `dead`, `partial`, `scheduled` or `cancelled`), `deliver_at` (RFC3339 UTC as submitted, except that a replace which releases the message at once
reports the release time, or `null` when none was given), `created_at`, `cancelled_at` (RFC3339 UTC, or `null` unless cancelled) and `deliveries[]`
(`id`, `destination`, `state`, `dead_reason`, `attempt_count`, `last_status_code`, `next_attempt_at`,
`delivered_at`). A delivery `state` is `pending`, `delivered`, `dead`, `scheduled` or `cancelled`;
`next_attempt_at` is set only while the delivery is `pending`.

### Idempotency and `wait`

- **Idempotency:** `(app_id, idempotency_key)` identifies a message. Re-submitting it returns the
  original message with `"duplicate": true` and never waits, so retrying after a timeout is safe. The
  one exception is a message still held by `deliver_at`, which a re-post with different content
  **replaces** (see Deferred send below).
- **`wait`:** `?wait=N` (whole seconds, 0-10, a larger value is clamped to 10) holds the response until
  every delivery has had a first attempt or the wait elapses. The message is already committed when the
  wait ends, so a short wait is not a failure: read the returned `deliveries[].state`. A non-integer or
  negative `wait` is `400 {"error":"invalid wait"}`. `wait` is ignored (an immediate `202`) when the
  resulting message is `scheduled`.

### Status endpoints

- `GET /v1/messages/{id}?app_id=<app>` returns `200 {"message": {...}}`; `app_id` is required
  (`400 app_id is required`) and another app's message is `404 message not found`.
- `GET /v1/messages?app_id=<app>&idempotency_key=<key>` looks a message up by key
  (`400 app_id and idempotency_key are required`).
- Both return a held message with `status` `scheduled` and a cancelled one with `status` `cancelled`;
  neither status is ever mixed with another, because a message moves between states as a whole.

### Deferred send (`deliver_at`)

Set `deliver_at` to have delivery-api hold the message and release it into the delivery queue when it is
due, instead of sending at once.

- **Format:** an RFC3339 timestamp with any offset; it is stored and returned in UTC. A malformed value is
  `400 invalid JSON body`. The zero time (`0001-01-01T00:00:00Z`), a time whose UTC form is before year 1,
  and a time after `9999-12-31T23:59:59Z` (reachable only through an offset, e.g.
  `9999-12-31T23:59:59-01:00`) are `400 {"error":"invalid deliver_at"}`.
- **Past or absent means immediate:** `deliver_at` absent, `null`, or at or before the server's current time
  sends immediately, exactly like a message without it (`status` `pending`). There is no upper horizon:
  any other future time is accepted.
- **Scheduled:** a future `deliver_at` answers `202` at once with `status` `scheduled`, every delivery in
  state `scheduled`, `deliver_at` set and `?wait=` ignored. Targets are resolved at submit, so a bad
  destination or route still fails fast with the same `400`/`409`/`422` as an immediate send. A release
  poller (about once a second) flips every due message to `pending` in batches of 100 messages, then the
  normal worker delivers it with the usual retries.
- **Destinations are re-checked at release:** a destination that is disabled or deleted between submit and
  `deliver_at` does not fail the submit retroactively. The delivery ends as a dead delivery with
  `dead_reason` `destination_disabled` or `destination_deleted`, visible in the operator Delivery log.
- **Per-app cap:** an app may hold at most 1000 scheduled messages (the operator can change this with
  `DELIVERY_MAX_SCHEDULED_PER_APP`, 1 to 100000). Scheduling one more is
  `429 {"error":"too many scheduled messages"}` and nothing is stored. A replace does not change the count,
  so it is never refused by the cap, and cancelling or releasing a message frees a slot.
- **Service-wide cap:** all apps together may hold at most 100000 scheduled messages (the operator can
  change this with `DELIVERY_MAX_SCHEDULED_TOTAL`, 1 to 10000000). Scheduling one more is
  `429 {"error":"scheduled message capacity reached"}` and nothing is stored; unlike the per-app cap, this
  is not something the app can fix by cancelling its own messages. If the app is also at its own cap,
  the per-app `too many scheduled messages` is reported. A replace is never refused by it either.
- **Replace until release:** re-posting the key of a `scheduled` message with different content (any of
  `channel`, `to`, `critical`, `deliver_at`, `payload` or `display`) replaces it in place: same
  `message_id` and `created_at`, targets re-resolved, response `202` with `"duplicate": false,
  "replaced": true`. If the new `deliver_at` is absent or not in the future, the message is released
  immediately (and `?wait=` then applies). If re-resolution fails the error is returned as for a new
  message and the original stays scheduled and unchanged. Re-posting **identical** content is
  `"duplicate": true, "replaced": false` without resolving targets, so a retry is safe even if a
  destination was disabled since. A replace that loses the race to a release or a cancel, and a
  concurrent insert of the same key, also return `"duplicate": true, "replaced": false` with the stored
  message.
- **Cancel is final:** re-posting the key of a `cancelled` message returns `"duplicate": true` with
  `status` `cancelled` and ignores the new content. A released message (`pending`, `delivered`, `dead` or
  `partial`) behaves the same way: `"duplicate": true`, content ignored.
- **Cancel:** `DELETE /v1/messages/{id}?app_id=<app>` or
  `DELETE /v1/messages?app_id=<app>&idempotency_key=<key>`. A missing parameter is
  `400 app_id is required` / `400 app_id and idempotency_key are required`, and a malformed `app_id` or
  `idempotency_key` is `400 invalid app_id` / `400 invalid idempotency_key`. Every other outcome is
  `200 {"message_id": "<id>", "cancelled": <bool>, "status": "<status>"}`, so the call is idempotent:

  | Situation                                            | `cancelled` | `status`        | `message_id`     |
  | ---------------------------------------------------- | ----------- | --------------- | ---------------- |
  | Message was `scheduled` (now cancelled)              | `true`      | `cancelled`     | the message id   |
  | Message already `cancelled`                          | `true`      | `cancelled`     | the message id   |
  | Already released (pending, delivered, dead, partial) | `false`     | its real status | the message id   |
  | Unknown id, non-UUID id, or another app's message    | `false`     | `not_found`     | the requested id |
  | By-key cancel with an unknown key                    | `false`     | `not_found`     | `""`             |

  Cancel and release serialize on the message, so exactly one wins, and a cancel never stops a delivery
  that was already released.
- **Staleness is the app's job:** delivery-api never re-evaluates a message's content when it releases it.
  If what the message says can become wrong before `deliver_at` (a due date that moved, a task that was
  completed), the app must cancel or replace it.
- **Retention:** a message that is `scheduled` or has a `pending` delivery is never deleted by the retention
  sweeper. A released message's retention age counts from the later of its `created_at` and `deliver_at`
  (a replace that releases it immediately sets `deliver_at` to the release time), and a cancelled
  message's from the time it was cancelled.

### Errors

| Status | Body `error`                                                                 | Meaning                                                    |
| ------ | ---------------------------------------------------------------------------- | ---------------------------------------------------------- |
| 400    | a validation message above, `invalid JSON body`, `invalid wait`, `unknown destination` | Fix the request                                  |
| 401    | `app key required`, `invalid app key`                                        | Missing or malformed `Authorization: Bearer dak_...`, or an unknown, revoked, unbound or past-grace key |
| 403    | `app_id does not match app key`                                              | The `app_id` names another app than the key's              |
| 409    | `destination disabled`                                                       | `to.destination` is disabled                               |
| 413    | `request body too large`                                                     | Body over 256 KiB                                          |
| 422    | `no destination matched`, `app has no ntfy publisher`, `destination type not yet supported` | No route/destination/publisher can take it |
| 429    | `too many scheduled messages`                                                | The app already holds the maximum scheduled messages (see Deferred send) |
| 429    | `scheduled message capacity reached`                                         | All apps together hold the maximum scheduled messages (see Deferred send) |
| 503    | `messages unavailable`, `identity unavailable`                               | delivery-api is up but cannot accept messages, or cannot check the key (fails closed) |
| 500    | `internal error`                                                             | Retry later; the cause is logged server-side               |

Through Cloudflare Access an unauthorized token gets a Cloudflare `403`/redirect before reaching the API.

### Recipients

`to.recipient` takes an **email or a `person_hash`** and sends the notification to that person's
personal topic. Only the hash is stored (an email is hashed on submit and never persisted). The topic
is `{app_id}-{person_hash}-{channel}`, the same topic the user is subscribed to after `POST /v1/provision`
(Step 2). Because ntfy caps topics at 64 characters, `app_id` + `channel` must keep the whole topic
within 64 characters (a longer combination is rejected at submit). The app also needs a **publisher**
(minted by the operator, see the runbook below); with none, the submit fails closed with **422**.

```
POST /v1/messages?wait=10
{
  "app_id": "urls4irl",
  "to": { "recipient": "user@example.com" },
  "channel": "all",
  "idempotency_key": "evt-123",
  "payload": {},
  "display": { "title": "Welcome", "body": "Thanks for joining" }
}
```

### Human-facing sinks (ntfy and Discord)

The ntfy and Discord sinks render only `display`, which needs a `title` or a `body` that is not blank
(400 otherwise; whitespace alone does not count).

- **ntfy:** `title` becomes the ntfy title and `body` the message (a title-only message is sent as the
  message with no title), `url` the click action, and `priority` maps to the ntfy `Priority`.
- **Discord:** posts plain content `**title**\nbody\nurl`, truncated to 2000 characters, with every
  mention suppressed. `priority` is ignored.

### `critical`

`critical: true` is recorded and returned on the message, and **changes no delivery behavior**: ntfy and
Discord output is the same as for a non-critical message (set `display.priority` for ntfy urgency). The
caller owns the fallback. On a non-2xx, a timeout, or a `wait` result whose delivery is not `delivered`,
post directly to the app's own fallback Discord webhook.

The direct-fallback pattern for a critical alert: submit with `?wait=10` and treat the alert as sent only
when the response is `202` and every `deliveries[].state` is `delivered`. On a non-2xx, a network error or
timeout, an empty `deliveries[]`, or any delivery still `pending` or `dead`, post the alert straight to
the fallback webhook. Re-submitting with the same `idempotency_key` is safe (a duplicate never waits), so
poll `GET /v1/messages/{id}?app_id=` instead of resubmitting if you want to confirm before falling back.

### Verifying webhook signatures (including during a rotation)

A webhook destination signs each delivery. Headers: `X-Delivery-Message-Id`, `X-Delivery-Idempotency-Key`,
`X-Delivery-Timestamp` (Unix seconds) and `X-Delivery-Signature`. The signature is `v1=` plus the hex
HMAC-SHA256, keyed with the signing secret, of `<timestamp>.<raw request body>`.

During a rotation overlap (the operator rotates the secret in the admin UI with an overlap of 1-168
hours) `X-Delivery-Signature` carries **two** comma-separated `v1=` values: one made with the new secret
and one with the previous secret, until the overlap expires. Verify by accepting the request when **any**
`v1=` value matches the HMAC computed with a secret you hold, using a constant-time comparison. Deploy
the new secret to the receiver before the overlap ends; after it ends only the new signature is sent.
A second rotation during an open overlap replaces the previous secret at once, so the earlier rotation's
secret stops being signed immediately.

### Heartbeats (dead-man's-switch checks)

A heartbeat check watches a recurring job (a cron entry, a systemd timer, a worker loop). The job pings
delivery-api each time it runs; if no ping arrives within `period + grace`, delivery-api marks the check
down and sends **one** down alert, then **one** recovery alert when the next ping arrives. Alerts go
through the normal message pipeline to the check's own destinations (1 to 20 of them, set when the
operator creates the check), as `channel: "heartbeat"` messages.

**The check is created first**, by your manifest (`heartbeats`, applied by your deploy through
`/v1/apps/{app_id}/heartbeats`) or by the operator (Heartbeats section of the Delivery tab, or
`POST /v1/admin/heartbeats` with the proxy token), with your `app_id`, a `name`, a `period_seconds`, a
`grace_seconds` and its destinations. A ping for a check that does not exist is a `404`; pings never
create one.

**Ping (success).** `POST https://notifs-delivery.4irl.app/v1/heartbeats/<app_id>/<name>`, with your app's
`CF-Access-Client-Id` / `CF-Access-Client-Secret` headers (the same service token as `POST /v1/messages`;
pings sit outside `/v1/admin`) and your delivery key as `Authorization: Bearer dak_...`, whose app must be
the `<app_id>` in the path (another app's id is `403 app_id does not match app key`). The body is ignored,
so a bare `POST` works:

```bash
curl -fsS -m 10 --retry 3 -X POST \
  -H "CF-Access-Client-Id: $NOTIFS_ID" -H "CF-Access-Client-Secret: $NOTIFS_SECRET" \
  -H "Authorization: Bearer $NOTIFS_KEY" \
  https://notifs-delivery.4irl.app/v1/heartbeats/myapp/nightly_backup
```

**Fail ping.** `POST .../v1/heartbeats/<app_id>/<name>/fail` reports that the job ran and failed. It marks
the check down at once (reason `fail`) instead of waiting out the deadline:

```bash
curl -fsS -m 10 --retry 3 -X POST \
  -H "CF-Access-Client-Id: $NOTIFS_ID" -H "CF-Access-Client-Secret: $NOTIFS_SECRET" \
  -H "Authorization: Bearer $NOTIFS_KEY" \
  https://notifs-delivery.4irl.app/v1/heartbeats/myapp/nightly_backup/fail
```

Both return `200` with the check's state after the ping:
`{"app_id":"myapp","name":"nightly_backup","state":"up"}`: `up` after a success ping, `down` after a fail
ping (a ping on a paused check is still recorded and answers `200`). Other answers: `400` for a
malformed `app_id` or `name` (`name` matches `^[a-z0-9][a-z0-9_]{0,62}$`), `401` `app key required` or
`invalid app key`, `403` `app_id does not match app key`, `404` `check not found`, `503` `heartbeats
unavailable` or `identity unavailable`.

Wire it to a job so a success ping is sent only when the job succeeds, and a fail ping when it fails:

```bash
# cron: success ping when the job exits 0, /fail ping otherwise (put the ping logic in a wrapper script)
0 3 * * * /opt/myapp/backup-and-ping.sh

# backup-and-ping.sh
BASE=https://notifs-delivery.4irl.app/v1/heartbeats/myapp/nightly_backup
if /opt/myapp/backup.sh; then SUFFIX=""; else SUFFIX="/fail"; fi
curl -fsS -m 10 --retry 3 -X POST -H "CF-Access-Client-Id: $NOTIFS_ID" -H "CF-Access-Client-Secret: $NOTIFS_SECRET" -H "Authorization: Bearer $(cat "$NOTIFS_KEY_FILE")" "$BASE$SUFFIX"
```

If the job cannot run at all (the host is down, cron stopped), no ping arrives and the check goes down when
`period + grace` has passed; `/fail` only makes a job that ran and failed immediate.

```ini
# systemd (needs systemd 249+): in backup.service, run by backup.timer
[Unit]
OnSuccess=ping-ok@nightly_backup.service
OnFailure=ping-fail@nightly_backup.service
```

```ini
# ping-ok@.service (ping-fail@.service is the same with /fail appended to the URL); %i is the check name
[Service]
Type=oneshot
# The delivery key changes on every deploy, so read it from the file the deploy writes, not from the unit.
# systemd treats $ specially in ExecStart, so the shell's $( is written $$(. The key file must be
# readable by the user the unit runs as.
ExecStart=/bin/sh -c 'exec /usr/bin/curl -fsS -m 10 --retry 3 -X POST -H "CF-Access-Client-Id: ..." -H "CF-Access-Client-Secret: ..." -H "Authorization: Bearer $$(cat /etc/myapp/notifs-key)" https://notifs-delivery.4irl.app/v1/heartbeats/myapp/%i'
```

**State model.** A check is `new` until its first ping, then `up`. It goes `down` when `now` passes
`anchor_at + period + grace` (the anchor is the last ping, or creation, or the last resume), or
immediately on a `/fail` ping. `late` is only a display state: the check is `up` but has passed its
`period`, still inside the grace window, and sends no alert. A `down` check that receives a success ping
goes `up` and sends the recovery alert; a missed deadline or a fail ping on a check that is already down
sends nothing more. A pause stops all alerts while it lasts (pings are still recorded, silently; an alert already being sent at the instant of the pause may still go out once) and a resume
restarts the deadline from the resume time.

**Choosing period and grace.** `period_seconds` is the interval between pings (60 s to 31 days);
`grace_seconds` is the slack added to it (60 s to 7 days). Set the period to the job's schedule and the
grace to the longest the job may reasonably run or slip. For a **daily** job use a grace of **at least 1
hour**: the schedule is period + grace only (no cron or timezone), so a daylight-saving shift or a
long run would otherwise raise a false down alert. After a delivery-api restart the checker waits one
minute before it marks anything down, so pings that the outage blocked can still arrive.

**A new check alerts if it is never pinged.** Creating a check starts its deadline immediately, so an
app that is never wired up (or pings the wrong `name`) goes `down` after `period + grace`. This is
deliberate: it is how a broken integration gets noticed. Remove or pause a check for a job that does not
run yet.

### Operator runbook: ntfy publisher for an app

Recipient deliveries publish with a per-app ntfy publisher token that delivery-api stores sealed. It is
minted once through provisioning-api by an explicit operator call, never lazily on submit:

- Mint with the operator's admin proxy token (the VPS-local `http://127.0.0.1:${DELIVERY_PORT}` form
  still works on the VPS itself):
  `curl -X POST https://notifs-delivery.4irl.app/v1/admin/apps/<app>/ntfy-publisher -H "CF-Access-Client-Id: <proxy-id>" -H "CF-Access-Client-Secret: <proxy-secret>"`
- Re-running the same call stores a new token in delivery-api, but it does **not** revoke the previous
  one. The mint is additive (`rotate:false`), so the previous publisher token stays valid in ntfy, with
  write access to the app's `{app_id}-*` topics. To revoke it, rotate the app's publisher in
  provisioning-api (the admin UI "remint with rotate"), then re-mint so delivery-api stores the new
  token. Rotating also invalidates every token the app itself holds for direct publishing.
- An admin-UI "remint with rotate" on the same app revokes delivery-api's token too. Recipient
  deliveries then dead-letter as `terminal_status` with a 401 until the publisher is re-minted.
- After `POST /v1/deprovision-app` the app's ntfy publisher user is gone, but delivery-api still holds
  its stored token, so recipient submits are accepted (202) and then dead-letter on a 401. Remove the
  stored publisher so submits fail closed (422, `app has no ntfy publisher`):
  `curl -X DELETE https://notifs-delivery.4irl.app/v1/admin/apps/<app>/ntfy-publisher` with the same two
  proxy-token headers (204, idempotent; it removes only delivery-api's copy and revokes nothing in ntfy).
  Re-mint the publisher before recipient sends are expected to work again.

### Operator runbook: exposing delivery-api and issuing app tokens

One-time setup, in this order, so the hostname is never reachable without an Access app in front of it:

1. In Zero Trust create one **Service Auth** Access application for `notifs-delivery.4irl.app` (policy: the
   admin proxy token only). Before onboarding the first app, split it into two path-scoped apps:
   `notifs-delivery.4irl.app/v1/admin` (the admin proxy token only) and `notifs-delivery.4irl.app` (the
   proxy token plus every per-app token).
2. Add a Tunnel public hostname `notifs-delivery.4irl.app` → `http://localhost:8300`.
3. Set the admin Pages binding `DELIVERY_API_URL` to `https://notifs-delivery.4irl.app`.
4. Add `/delivery` to the `notifs-admin` Access **Bypass** app, then verify with a non-GET (disable then
   enable a test destination in the Delivery tab: 200, not 302/405).

Per app: create a service token named e.g. `<app> → notifs-delivery`, add it to the whole-host
`notifs-delivery` policy **only** (never the `/v1/admin` one), and give the app the id and secret. Confirm an
app token gets 403 on `/v1/admin/destinations`, and that `POST /v1/messages` is `401 app key required`
without a delivery key and `202` with the app's key (the next runbook issues it).

### Operator runbook: app identities

An app proves which app it is with a delivery key its deploy workflow obtains after one approval from you
(model: "App identity: how an app proves who it is"). All of this happens in the Delivery tab of the admin
UI, **App identities** section (the same actions exist as `/v1/admin/app-identity/*`; there is no CLI).
Never run `make onboard app=<id> cmd=verify` for an app manifest in production: it needs a delivery key that
no operator command can supply there, and it is dev/test-only.

**Approve a new app (once per app).**
1. Authorize the app's Access token on the whole-host `notifs-delivery` app (never `/v1/admin`; runbook
   above) and give the app the id and secret.
2. After the app's first deploy fails with "identity request #N awaits operator approval", open
   **Requests**. Read the row: the `app_id` is whatever the caller claimed, but the repository, workflow
   file, ref and environment under **Requested by (GitHub)** are verified by GitHub's signature. Approve only
   if they are the repository, workflow, branch and environment you expect for that app.
3. **Approve.** The app now appears under **Approved apps**; its next exchange (re-run the deploy job) gets
   a key. `409 app already bound` or `workflow identity already bound` means the app id or that workflow
   identity is already taken (unbind the other binding first if that is intended).

**Reject a request.** **Reject** (then confirm) for a repository or workflow you do not recognise. The
deploy keeps failing; the rejected request stays listed for 30 days, and a deploy with the same claims
files a new pending request, so also remove the app's Access token if you want it to stop for good.

**The 429 pending-request cap.** At most **20** requests may be pending in total, across all apps, and a
21st exchange is `429 too many pending identity requests` (the app's deploy fails with that text). There is
no per-repository or per-app cap and none is planned: any GitHub repository can mint a token for the
default audience, so a repository that holds an authorized Access token (or a leaked one) can fill the
queue. Clear it by rejecting the stale or unwanted requests in **Requests**; rejected requests do not count.
Requests nobody decides also expire on their own: the periodic cleanup deletes a pending request that has
gone `DELIVERY_PENDING_IDENTITY_REQUEST_DAYS` (7 by default, integer 1-365) without a refresh, which
frees its slot. A legitimate app whose request was dropped by the cap or expired simply re-deploys.

**Revoke a key.** Under **Approved apps**, **Revoke** on a key (idempotent). That key stops working at once
(`401 invalid app key`). It does not unbind the app: its next deploy mints a new key. If a key leaked,
revoke both the active key and any superseded one that is still within its 24 h grace, then redeploy; if
the workflow itself is compromised, unbind the app instead.

**Unbind an app.** **Unbind** (idempotent) deletes the binding and every key with it, in one statement, so
all of the app's keys stop at once. The destinations, routes and checks it owned are kept (still owned by
that `app_id`, and not manageable through the app API until the app is bound again); delete them through
the admin UI if the app is going away. The app's next deploy files a fresh pending request. This is also
how to change an approved identity (renamed workflow file, a new branch or environment): unbind, deploy,
approve the new request. Unbind does not touch the Access token; remove that too to cut an app off.

**Name and key collisions.** Because the `<app_id>_` prefix rule does not tell `app` from `app_a`, app
`app` can create a destination named `app_a_hook` and leave `app_a` with `409 destination already exists`
(an availability collision only: the row is owned by `app`, and `app_a` can never use it). The same `409`
appears when an app's route (`app_pattern` + `channel_pattern`) or check (`app_id` + `name`) is already
held by an operator row. To remediate, delete the squatting row through the admin API or UI (a destination
a route or check uses cannot be deleted until those are removed first), and have the app re-apply; or have
the app pick another name.

**Behaviour to know.**
- **JWKS cache.** delivery-api fetches GitHub's signing keys on the first exchange and keeps them in memory.
  An unknown `kid` triggers one refetch, at most once a minute (a failed fetch also counts), so GitHub
  rotating its keys needs no restart. While GitHub's keys cannot be fetched, exchanges are `503 identity
  unavailable` (log line `oidc jwks fetch failed`); keys already issued keep working because key checks
  use only Postgres.
- **Audience.** The server's `DELIVERY_OIDC_AUDIENCE` defaults to `notifs-delivery`. If you change it, roll
  the matching `audience` input out to every app's workflow at the same time, or every exchange is `401`.
- **Dev-mode bypass.** `DELIVERY_OIDC_DEV_MODE` and `DELIVERY_OIDC_JWKS_FILE` exist only so the local and
  CI stacks can sign their own test tokens (`make dev-oidc-keys`). With them set, GitHub's keys are not
  trusted and whoever holds the dev private key can mint any app identity. **Never set either in production**:
  `docker-compose.prod.yml` sets only `DELIVERY_OIDC_AUDIENCE`, delivery-api refuses to start if the file is
  set without the flag, and a Go test fails if the prod compose file mentions either variable. A dev-mode
  instance logs `oidc verifier uses a static dev JWKS file` at startup.
- **Key rotation on every exchange**, dry runs included (`apply: 'false'`); the action exchanges before it
  lints the manifest. Every superseded key stays valid for 24 h from the moment it was superseded, so
  several deploys or dry runs in a day leave several valid keys at once (each shows as **Superseded**);
  the newest is the **Active** one the app host should be holding.

---

## Subscriber login — the generated password

The subscriber token (Step 2/Step 4) is what a **programmatic or Android** client uses to subscribe.
**iOS is different:** the ntfy iOS app's subscribe screen has **no token field**, so an iOS user signs into
the ntfy app with a **username + password**. The username is the derived ntfy user id `u_<person_hash>`
(the `user_id` from `/v1/provision`). The password is **generated by the notifications service**, never
chosen by the person or your app: a typeable `xxxx-xxxx-xxxx-xxxx` code (about 80 bits). The shared setup
page also asks Android users for this username and password, so every person you link to it needs one.

### Getting the password

`POST /v1/provision` returns `password` in its response **only** when that call generated one (the first
time a person is provisioned):

```json
{
  "user_id": "u_v4sf4e5teivpe3zi",
  "app_id": "urls4irl",
  "person_hash": "v4sf4e5teivpe3zi",
  "topic_pattern": "urls4irl-v4sf4e5teivpe3zi-*",
  "broadcast_topic": "urls4irl-broadcast",
  "token": "tk_<subscriber-token>",
  "password": "k7mq-x2pw-9rnd-ft4h"
}
```

- **Show it once.** Never store it, never log it, and pass the `Cache-Control: no-store` header through
  (every `200` from `/v1/provision` and `/v1/reset-password` carries it). It is not retrievable later.
- **Always display a returned password.** A person who existed before this change has no stored date and
  is **rotated** on their next provision: the returned password is the only delivery of the new one.
- **No `password` in the response** is normal (the person already has one, or the person store was
  unavailable). Do not treat it as an error; offer "Generate a new password" instead.
- **Recovery.** If a first-time provision's password never reached the person (for example the response
  was lost after the service recorded it), call `POST /v1/reset-password` to get a new one.

The person then logs into the ntfy iOS app with **username `u_v4sf4e5teivpe3zi` + that password** (after
setting the Default server to `https://notifs.4irl.app`; the setup page below walks them through it), and
their existing read ACL lets them subscribe to their own `{app_id}-{person_hash}-*` topics and
`{app_id}-broadcast`.

### `POST /v1/reset-password` — generate a new password

```
POST https://notifs-api.4irl.app/v1/reset-password
Content-Type: application/json
CF-Access-Client-Id: <id>
CF-Access-Client-Secret: <secret>

{ "app_id": "urls4irl", "email": "alice@example.com" }
```

| Status | Body | Notes |
| ------ | ---- | ----- |
| `200` | `{ "user_id", "app_id", "password" }` | The new password, shown once. `Cache-Control: no-store`. |
| `400` | `{ "error": "invalid JSON body" }` / `"invalid app_id"` / `"invalid email"` | Validated in that order. |
| `404` | `{ "error": "user does not exist" }` | The person is not provisioned. |
| `429` | `{ "error": "too many password changes" }` | Has a `Retry-After` header (seconds). |
| `500` | `{ "error": "internal error" }` | |

Every response from this endpoint (200, 400, 404, 429, 500) carries `Cache-Control: no-store`.

- **Limit: 5 resets per person per rolling hour.** A failed reset does not count against it.
- **`429` takes precedence over `404`.** The limit is checked first, so an unknown email at the limit, or
  any request while the limit store is down, still answers `429`.
- **`Retry-After` is on every `429`**, with the same status and body. For a true limit it is the seconds
  until the oldest slot in the window expires; for a limit-store outage it is a fixed 60. Callers cannot
  tell the two apart, so simply wait that long.
- **Warn first.** A reset replaces any earlier password (including one the person is already using on
  another device), so say so before the call, as the admin UI does: its Users list has a "Reset password"
  row action that asks for confirmation first, then reveals the new password once. Operators can use it
  instead of calling this endpoint by hand; it is subject to the same limit.
- The password is applied to the person, not to one app: resetting from any app changes the one ntfy
  password. Any app's service token can currently reset any person (out of scope here; the
  app-registration work will bind tokens to `app_id`).

### Suggested UI copy

Use this copy so apps do not each invent their own. It is a suggestion: reword it as long as it says the
password is shown once and that it replaces the old one.

| Where | Text |
| ----- | ---- |
| Reveal lead | "This password is shown only once. It replaces any earlier password for this person." (name your own subject) |
| No-password state | "No password was returned by this request." plus a pointer: "call POST /v1/reset-password from the app that serves them" |
| Warn-first, before a reset | "Generating a new password replaces the current one." |
| `429` wait message | "Too many password changes. Try again in `<Retry-After>` s." (the abbreviation reads correctly for any count, avoiding "1 seconds") |

Accessibility:
- Announce a late-arriving result through a **single persistent `role="status"` region**: one region,
  rendered before the content, with `aria-atomic="false"`, and a fresh node for each later message such as
  "Copied", so the reveal lead is not re-read.
- Show the password in a **labelled read-only field**, not inside a live region.
- Do not move focus programmatically on reveal.

### `POST /v1/set-password` — not for new apps

`set-password` still exists for apps that let a person choose their own password, but **new apps should
use the generated password**. It records that the person has a password (`password_set_at`) before
changing it, so a later `POST /v1/provision` will not replace a chosen password. It returns `500` and
changes nothing when that record cannot be written (person store unavailable). The password policy is 8–128
characters (`400 { "error": "invalid password" }`), and the user must already be provisioned
(`404 { "error": "user does not exist" }`). See the API reference for the full contract.

- **Android/programmatic clients don't need a password** — they authenticate with the subscriber token.
  Only the iOS app (and any other username+password ntfy client, including the setup page's flow) needs one.

---

## Link your users to the setup page

Do not write your own ntfy install or subscribe instructions. Send users to the shared, public setup
page at `https://notifs-setup.4irl.app`. It detects iPhone or Android, walks them through installing
and configuring the ntfy app (iOS Default server, login, subscribing), shows each topic with a copy
button, and offers one-tap `ntfy://` subscribe buttons on Android. It makes no API calls.

**Link format** (all three values are query parameters):

```
https://notifs-setup.4irl.app/?app=tasktracker&user=u_v4sf4e5teivpe3zi&name=Task%20Tracker
```

| Param  | Required | Rule                                                                                                                                                                                                                     |
| ------ | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `app`  | yes      | Your `app_id`: `^[a-z0-9][a-z0-9_]{0,62}$`, and not the reserved word `everyone`.                                                                                                                                        |
| `user` | yes      | The `user_id` from `POST /v1/provision`: `^u_[a-z2-7]{16}$`. This is the user's ntfy **username**.                                                                                                                       |
| `name` | no       | Display name shown on the page, URL-encoded. After trimming, 1-64 characters with no control or format characters (such as bidi overrides); otherwise (missing, blank, too long or containing any) the page shows `app`. |

- The page derives the topics itself from `app` and `user`: `{app}-{hash}-all` (where `hash` is `user`
  without its `u_` prefix) and `{app}-broadcast`, on the constant server `https://notifs.4irl.app`.
  Never put topics or a server in the link; unknown parameters are ignored.
- If `app` or `user` is missing or invalid, the page shows an "incomplete link" message and nothing
  else. It never echoes the bad value.
- **The password is never in the URL.** The page shows the username and tells the user to type the
  generated password you showed them (see "Subscriber login — the generated password") into the ntfy
  app. This applies to iPhone **and** Android users who follow the page, so make sure the person has
  been shown their password before you link them to it. Never put the password, or any token, in the
  link.
- Pass the `user_id` from `POST /v1/provision` as the `user` parameter (it is the ntfy username), so
  the username the page shows is the one your user logs in with.
- The page requires JavaScript.

---

## Usage recommendations (this deployment)

The 5 steps above are the mechanics; this is how to use them well against `notifs.4irl.app`.

**Delivery: prefer client-direct.** This deployment is built for client-direct delivery — the user's
device subscribes to ntfy itself. Reach for backend-relay only when a token must never live on the
client; it makes your backend own the reconnection/offline story ntfy otherwise handles for free.

**iOS (ntfy app) — the Default server gotcha.** iOS can't hold a background connection, so iOS delivery
goes through APNs bridged via ntfy.sh. Two consequences for you:
- The user's ntfy iOS app **Default server** (or per-subscription server) must equal
  `https://notifs.4irl.app` **exactly** — https, no trailing slash. Any mismatch and the wake-up can't
  be matched, so notifications **silently never arrive**. The shared setup page walks iOS users
  through this; link them to it (see "Link your users to the setup page") and do not write your own
  Default server instructions.
- Expect a short APNs wake latency on iOS; Android/desktop/web are instant. Don't design flows that
  assume sub-second iOS delivery.

**Periodic / bulk sends (e.g. hourly digests).** Publishing is one POST per user-topic — cheap at this
scale, but mind two limits:
- A backend publishing many messages from one IP looks like a single ntfy **visitor** and will hit the
  per-visitor rate limit. Ask the operator to raise/exempt your publisher before a bulk job.
- Each **iOS-bound** message also costs one upstream poll to ntfy.sh (rate-limited) — the operator sets
  `upstream-access-token`.
- **Coalesce** many-per-user notifications into a single **digest** message (published on
  `{app}-{hash}-all` with a digest `Title`) instead of N separate ones — fewer requests and better UX.

**Missed-message catch-up.** The server retains messages for **24h**. When a client reconnects after
downtime, backfill with `since=`:
`GET https://notifs.4irl.app/{topic}/json?since=<id|duration|timestamp>` (up to 24h back). Don't rely on
a live stream alone to guarantee delivery across reconnects.

**Notification types & user preferences.** Model each notif type as a `Title` / `Tags` on the
single `{app_id}-{person_hash}-all` channel. Enforce user preferences at **publish time** — simply
don't emit a type the user disabled — so a broad subscriber token can't be used to receive muted
types.

---

## API reference — `notifs-api.4irl.app`

All calls require the two `CF-Access-Client-*` headers. All bodies + responses are JSON.

| Method & path                | Body                                                     | Success response                                                          |
| ---------------------------- | -------------------------------------------------------- | ------------------------------------------------------------------------- |
| `POST /v1/provision-app`     | `{ "app_id", "rotate"? }`                                | `{ app_id, publisher_user_id, topic_pattern, token }`                     |
| `POST /v1/provision`         | `{ "app_id", "email" }`                                  | `{ user_id, app_id, person_hash, topic_pattern, broadcast_topic, token, password? }` |
| `POST /v1/reset-password`    | `{ "app_id", "email" }`                                  | `{ user_id, app_id, password }`                                           |
| `POST /v1/deprovision`       | `{ "app_id", "email" }` **or** `{ "app_id", "user_id" }` | `{ user_id, app_id, removed }`                                            |
| `POST /v1/set-password`      | `{ "app_id", "email", "password" }` (not for new apps)   | `{ user_id, app_id, updated }`                                            |
| `POST /v1/deprovision-app`   | `{ "app_id" }`                                           | `{ app_id, removed }`                                                     |
| `GET /v1/users`              | —                                                        | `{ users: [{ user_id, apps, topic_patterns }] }`                          |
| `DELETE /v1/users/{user_id}` | —                                                        | `{ user_id, deleted }`                                                    |
| `GET /healthz`               | —                                                        | `ok` (behind Access)                                                      |

Notes:
- `user_id` is always the derived ntfy id `u_<person_hash>`. `email` must be a valid address
  (validated after trim + lowercase — case-insensitive; `Alice@x.com` == `alice@x.com`).
- Errors are `{ "error": "<message>" }` with the matching HTTP status (`400` invalid input,
  `404` user does not exist, `403`/redirect = missing/invalid service token, `500` internal).
- **`rotate` on `/v1/provision-app`** (optional, default `false`): `false`/omitted mints an *additional*
  publisher token and leaves existing ones valid (safe, zero-downtime rotation by-issuing — see Step 1's
  note). `true` is a **hard rotation**: it revokes every existing publisher token first, then mints one
  fresh — for a leaked credential. The old token stops working immediately, so the app stops publishing
  until it is redeployed with the new token.
- **`DELETE /v1/users/{user_id}` is idempotent**: it returns `200 { user_id, deleted: true }` even when
  the ntfy user is already gone (it no longer `404`s for an absent user), and it also removes the
  person-service reverse-index row. Do not branch on a `404` from this endpoint.
- **`POST /v1/provision`** also returns `password` (`xxxx-xxxx-xxxx-xxxx`) **only** when this call
  generated one; otherwise the field is absent. Every `200` carries `Cache-Control: no-store`. See
  "Subscriber login — the generated password".
- **`POST /v1/reset-password`** makes a new generated password (see "Subscriber login — the generated password"). `200
  { user_id, app_id, password }`; `400` (`invalid JSON body` / `invalid app_id` / `invalid email`, in that
  order); `404 { "error": "user does not exist" }`; `429 { "error": "too many password changes" }` with
  a `Retry-After` header; `500 { "error": "internal error" }`. Every response from it carries
  `Cache-Control: no-store`.
- **`POST /v1/set-password`** is **not for new apps**; use the generated password instead. It sets a
  person's ntfy password to one your app chose. The user **must already be provisioned**: setting a
  password on a not-yet-provisioned email returns `404 { "error": "user does not exist" }`. The password
  is validated at **8–128 characters** (`400 { "error": "invalid password" }` otherwise; `app_id` and
  `email` are validated first, in that order). It records that the person has a password
  (`password_set_at`) before changing it, so a later `POST /v1/provision` will not replace a chosen
  password; if that record cannot be written (person store unavailable) it returns `500` and changes
  nothing. `change-pass` overwrites any existing password, so calling it again simply resets the
  credential. The response `{ user_id, app_id, updated }` echoes the derived `user_id`
  (`u_<person_hash>`) with `updated: true`.
- `POST /v1/deprovision-app` fully removes an app: its publisher identity, every subscriber's grant for
  the app, and the app-registry row. It is idempotent.
- `GET /v1/users`, `DELETE /v1/users/{user_id}`, and `/v1/deprovision-app` are cross-app management
  surfaces — most app integrations only need `provision-app`, `provision`, and `deprovision`.

## ntfy reference — `notifs.4irl.app`

- **Publish:** `POST https://notifs.4irl.app/{topic}` with `Authorization: Bearer <publisher-token>`
  and the message in the body (ntfy headers `Title`, `Priority`, `Tags`, `Click`, `Actions`, … all apply).
- **Subscribe:** `GET https://notifs.4irl.app/{topic}/json` (or `/sse`, or WebSocket) with
  `Authorization: Bearer <subscriber-token>`.
- ntfy is **not** behind Cloudflare Access — its own token auth is the boundary. Never send the
  `CF-Access-Client-*` headers here; use the `Bearer` token.

---

## Integration checklist

### delivery-api and heartbeats (see "Onboarding a new app")

- [ ] Operator confirmed the `/v1/admin` Access split, then issued a token authorized on the whole-host
      `notifs-delivery` app only.
- [ ] The app's repo has its manifest and the `notifs-app` deploy step (`permissions: id-token: write`,
      pinned to a full SHA), and the operator approved the app's identity request.
- [ ] Destinations and routes exist for the app (from its manifest), and a destination send-test arrived.
- [ ] Backend stores the `notifs-delivery` Client ID and Secret as secrets, and the delivery key (`dak_...`)
      as a file secret that every deploy replaces; every call sends `Authorization: Bearer`.
- [ ] First `POST /v1/messages?wait=10` returned `202` with every `deliveries[].state` `delivered`.
- [ ] `idempotency_key` is stable per event; critical alerts have the direct fallback.
- [ ] (Recipients) The app's ntfy publisher is minted and users are provisioned.
- [ ] One heartbeat check per recurring job, with a grace of at least 1 hour for daily jobs.
- [ ] Each job sends a success ping (and a `/fail` ping where it can tell), and the check moved from
      Waiting to Up after one run.

### ntfy direct

- [ ] Operator issued your app a Cloudflare Access service token, authorized on `notifs-api`.
- [ ] Backend stores `CF-Access-Client-Id` / `CF-Access-Client-Secret` as secrets.
- [ ] Called `POST /v1/provision-app` once; stored the **publisher token**.
- [ ] On user opt-in: call `POST /v1/provision`; store **`person_hash`** (+ subscriber token or
      deliver it to the client). Do this once per user, not per login.
- [ ] Publish to `{app_id}-{person_hash}-all` with the publisher token, and set a `Title`.
- [ ] User's client subscribes to `{app_id}-{person_hash}-all` with the subscriber token.
- [ ] (Optional) Site-wide announcements: publish to `{app_id}-broadcast` with the publisher token;
      clients subscribe to `{app_id}-broadcast` (read grant added automatically at provision, returned as `broadcast_topic`).
- [ ] On opt-out / account deletion: call `POST /v1/deprovision`.
- [ ] Always display a `password` returned by `POST /v1/provision` (shown once, never stored or logged,
      `Cache-Control: no-store` honoured), and offer "Generate a new password" via `POST /v1/reset-password`
      with the warn-first line and the `429` wait message (see "Subscriber login — the generated password").
- [ ] Once the person has their password (iPhone and Android users both type it into the ntfy app), link
      them to the shared setup page (see "Link your users to the setup page"); do not write your own
      Default server instructions.
- [ ] Link users to `https://notifs-setup.4irl.app/?app=<app_id>&user=<user_id>&name=<display name>` instead of
      writing your own ntfy install instructions.
- [ ] Bulk/periodic sender: publisher rate-limit raised/exempted by operator; many-per-user sends coalesced into a digest.
- [ ] All mint responses (`token`) are captured on first response — they are never re-shown.

## Related docs
- `ARCHITECTURE.md` — how the service works + Cloudflare/auth config, including the heartbeat watchdog
  operator runbook and the debugging quick reference.
- `README.md` — components, the local stack and delivery-api's runtime configuration.
- `admin-ui-same-origin` plan (`~/code/plans/4irl-notifs/`) — the admin console architecture.
