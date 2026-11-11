# Integrating an app with 4irl-notifs

**Audience:** an engineer (or an LLM) wiring a new client application (e.g. `urls4irl`,
`chores4irl`) into the 4IRL notification service so it can send personalized notifications to its
users, deliver alerts through retries and a delivery log, schedule messages for later, and monitor its
recurring jobs. This is a **self-service integration guide** — it is not about deploying the
notification service itself (that is `ARCHITECTURE.md`).

If you are deciding whether to integrate, read **Why integrate**. If you are integrating, read
**Onboarding a new app** first: it says what the operator does, what your app does, and in what order.
If the app already sends its own Discord, webhook or ntfy messages or reports from its own cron jobs, read
**Migrating an existing app from its own senders**: a generic, lane-by-lane playbook with reference
clients in Python and shell.
If you are a program or an LLM agent calling delivery-api, jump to **Agent quick reference**: every
endpoint with its request, response and status codes, and a copy-paste onboarding recipe. To provision
users for push notifications, also read the **Mental model** and **Provisioning your users through the
door** sections.

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
replacement for provisioning your users (see "Provisioning your users through the door") or for the
one-time operator setup in "Operator steps (once per app)" below (the Access split, service tokens, the
app's ntfy publisher, approving the app's identity) — it only covers the destinations/routes/heartbeats
part of onboarding.

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
- `heartbeats`: `{ "name", "period", "grace", "destinations"? }`, with `period`/`grace` as durations
  (`24h`, `1h`, …) rather than raw seconds. `destinations` is optional: omitted or `[]` means none
  (at most 20; routes still need 1 to 20).
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
`/v1/apps/{app_id}/…`. That includes `POST /v1/apps/{app_id}/{provision,deprovision,reset-password,set-password,ntfy-publisher}`,
so one key covers everything an app does. The key's app is the caller; an `app_id` in the body, the query or the path that
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

#### Registrars: one workflow, many apps

One approved workflow identity binds exactly one `app_id`. A monorepo or hosting platform that deploys many
apps from one workflow (the `GPropersi/apps` platform is the intended user) would need an approval click per
app, and could register only its first. A **registrar** removes that: the operator approves the workflow
identity **once**, together with a reserved **prefix** (for example `hosted_`). After that the workflow
exchanges its OIDC token plus a bare app **name**, and every app it names gets its own `dak_` key with no
further approval.

- **Who it is for.** A trusted deploy workflow that creates apps on demand. A normal single-app repo keeps
  the per-app flow above and never needs a registrar.
- **Sending a name.** Post `{"name","token"}` instead of `{"app_id","token"}`; send exactly one of the two
  (`400 send exactly one of app_id or name` otherwise). A name must match `^[a-z0-9][a-z0-9_]*$`
  (`400 invalid name`).
- **Derived id and length.** The server computes `app_id = prefix + name`; the registrar never names a full
  app id. The result must be a valid app id, at most **63 characters** in total (`400 invalid name` when the
  name makes it longer). Every derived id is a normal app: destinations, routes, heartbeats, the ntfy
  publisher and teardown all work as for any other app.
- **First request.** A name from a workflow identity that matches no registrar and no binding files a
  pending **registrar request** (`202 {"status":"pending","request_id"}`), in the same queue and under the
  same 20-request cap as app requests. The operator approves it as a registrar with a prefix.
- **Auto-create.** After approval a new name is created with no click: the person-service row, the
  binding and the key arrive in one `200 {"status":"issued","app_id":"<prefix+name>","key","key_id"}`. The
  operator sees it as an informational **New** row.
- **Ownership.** A registrar can mint keys only for apps it created, inside its own prefix. A derived id that
  is bound normally or by another registrar is `403 app id is not owned by this registrar`. The normal
  per-repo flow can never claim an id inside a registrar prefix (`409 app id is reserved by a registrar`),
  and a registrar's own token sent with an `app_id` is `403 identity does not match the approved binding`.
  A registrar never learns about other apps or registrars.
- **Cap.** At most **50** apps per registrar (`409 registrar app limit reached`).
- **Overlap.** A new name is refused with `409 app id overlaps an approved app id` when it would be an
  underscore-extension of a bound app id or the reverse (`hosted_a` and `hosted_a_b` cannot both exist).
- **Retryable race.** `409 registration changed, retry` means the app was unbound or created concurrently;
  exchange again with a fresh token.
- **Unbind recreates.** Unbinding one registrar app is not a block: the registrar recreates it (as New
  again) on its next deploy. Permanent removal is the platform's job (teardown, then stop deploying the name).
- **Deleting a registrar** unbinds every app it created (their keys stop; owned objects stay, exactly as for
  Unbind). The workflow identity can then be requested and approved again.
- **One identity, one role.** A workflow identity bound as a normal app cannot become a registrar
  (approving it as one is `409 workflow identity already bound`, and a bound app's token sending a `name` is
  `403 identity does not match the approved binding`); a registrar's identity is never a normal app.

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
   **Registrar mode:** a workflow approved as a registrar passes `name` (env `NOTIFS_APP_NAME`) instead of
   `app_id`; exactly one of the two is required, otherwise the action fails before any network call. On `200`
   the outputs are `key` (masked), `key_id` and `app_id` (the derived `prefix + name`); on `202` it fails with
   `registrar request #N awaits operator approval in the admin UI (App identities → approve as registrar)`.
   A manifest whose `app_id` differs from the derived id fails. The action requests the OIDC token, exchanges it,
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
| Push notifications straight to users' devices             | Provision users through the door (`### Provision a user`, with the app's `dak_` key) | A service token on `notifs-delivery` and the app's identity approval | "Provisioning your users through the door" |
| Send alerts and notifications with retries, a log, fan-out | `POST /v1/messages`      | A service token on the whole-host `notifs-delivery` app and the approval of the app's identity; destinations and routes come from the app's manifest (or the operator) | "Sending through delivery-api"   |
| Notify one person by email                                | `to.recipient`           | The above, plus the app's ntfy publisher (the app mints it itself, or you do; users are provisioned as in "Provision a user") | "Recipients"                     |
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
   (never the `/v1/admin` one); the same token serves provisioning users through the door. Hand over the
   Client ID and Client Secret; they are shown once.
3. **Approve the app's identity.** After the app's first deploy fails with "identity request #N awaits
   operator approval", open the Delivery tab, **App identities**, check that the requested repository,
   workflow file, ref and environment are the ones you expect, and approve. Full procedure, including
   reject, revoke and unbind: "Operator runbook: app identities".
4. **Destinations and routes.** Normally the app creates its own from its manifest. To make them yourself
   instead (operator-owned, usable by every app), add the destinations the app's messages should reach
   (ntfy, Discord or webhook) and the routes that send the app's traffic to them in the Delivery tab. Use the
   destination's send-test to confirm each one. Without a matching route or destination a send with no
   `to` fails with `422 no destination matched`, unless the operator has turned on holding
   (`DELIVERY_HOLD_UNROUTED`, off by default), in which case an app-key send is accepted as `202` with
   status `unrouted` (see "Unrouted messages (held)"). A route match whose destinations are all disabled
   counts as no match and is held the same way; a `to.destination` that is disabled or unknown is still
   `409`/`400`.
5. **If the app sends to people (`to.recipient`):** the app mints its own publisher (self-serve, see "Mint
   or rotate the publisher"), or you mint it for the app (see "Operator runbook: ntfy publisher for an
   app").
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
6. **Provision users through the door.** When a user enables notifications, call `POST /v1/apps/{app_id}/provision`
   with your `dak_` key (see "Provision a user" under "Provisioning your users through the door"), and
   deprovision when they disable them.
7. **Mint the publisher** if you send to people with `to.recipient` (see "Mint or rotate the publisher").
8. **If you message people, link them to the shared setup page** instead of writing your own ntfy
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
- **Removing an app:** call the operator cascade `POST /v1/admin/apps/{app_id}/deprovision` with body
  `{"app_id":"<app>"}` (repeating the path id). It is irreversible: it tears down the app's provisioning
  grants and users, unbinds its identity (every key stops at once), deletes the stored publisher and
  deletes the app's checks, routes and destinations. It answers `409` while a publisher mint is in
  progress and `502 "provisioning-api teardown failed"` if the teardown fails, and `503` if teardown is unavailable (retry both). Then remove the
  app's token from the Access policies. For planned maintenance without removal, unbind or pause instead.

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
| `POST /v1/app-identity/exchange` (Access token only, no key) | `{"app_id","token":"<GitHub OIDC JWT>"}` or, for a registrar workflow, `{"name","token":"<GitHub OIDC JWT>"}` (exactly one of `app_id` / `name`) | `202 {"status":"pending","request_id"}` (awaits operator approval) or `200 {"status":"issued","app_id","key","key_id"}` (the plaintext key, once; `app_id` is `prefix + name` in registrar form) | 400 malformed body, `app_id`, `invalid name` or `send exactly one of app_id or name`, 401 `invalid identity token`, 403 `identity does not match the approved binding` / `app id is not owned by this registrar`, 409 `app id is reserved by a registrar` (normal form, id inside a registrar prefix) / `registrar app limit reached` / `app id overlaps an approved app id` / `registration changed, retry`, 429 `too many pending identity requests`, 502 / 503 `app registry unavailable` (registrar auto-create, person-service), 503 `identity unavailable`. The `notifs-app` action calls this for you. |
| `POST /v1/messages` (optional `?wait=0..10`)                 | envelope JSON (see Envelope)  | `202 {"message":{…},"duplicate":bool,"replaced":bool}` (status `unrouted`, with `deliveries: []`, when holding is on and no route matched) | 400 validation, 409 `destination disabled`, 413, 422 `no destination matched` (holding off) / `app has no ntfy publisher` / `personal destination not available for recipient`, 429 (`too many held messages`, `held message capacity reached`, scheduled caps), 503 `messages unavailable` |
| `GET /v1/messages/{id}?app_id=<app>`                         | none                          | `200 {"message":{…}}`                                    | 400 `app_id is required`, 404 `message not found`                               |
| `GET /v1/messages?app_id=<app>&idempotency_key=<key>`        | none                          | `200 {"message":{…}}`                                    | 400, 404                                                                        |
| `DELETE /v1/messages/{id}?app_id=<app>` or `DELETE /v1/messages?app_id=<app>&idempotency_key=<key>` | none | `200 {"message_id","cancelled":bool,"status"}` (idempotent) | 400                                          |
| `POST /v1/heartbeats/{app_id}/{name}`                        | none                          | `200 {"app_id","name","state"}`                          | 400, 404 `check not found`, 503 `heartbeats unavailable`                        |
| `POST /v1/heartbeats/{app_id}/{name}/fail`                   | none                          | `200 {"app_id","name","state"}` (state `down`)           | same                                                                            |
| `POST /v1/apps/{app_id}/provision`                           | `{"email"}`                   | `200 {user_id, app_id, person_hash, topic_pattern, broadcast_topic, token, password?}` (`Cache-Control: no-store`) | 400, 401, 403, 429, 502 `provisioning request failed`, 503 `provisioning unavailable` (see "Door errors") |
| `POST /v1/apps/{app_id}/deprovision`                         | `{"email"}` or `{"person_hash"}` | `200 {user_id, app_id, removed}`                      | as provision, plus 404 `user does not exist` (treat as already removed) |
| `POST /v1/apps/{app_id}/reset-password`                      | `{"email"}`                   | `200 {user_id, app_id, password}` (`Cache-Control: no-store`) | as provision, plus 404 `user does not exist`, 429 with `Retry-After`, 503 `reset limit unavailable` |
| `POST /v1/apps/{app_id}/set-password`                        | `{"email","password"}` (legacy, not for new apps) | `200 {user_id, app_id, updated}`  | as provision, plus 404 `user does not exist` |
| `POST /v1/apps/{app_id}/ntfy-publisher`                      | `{}` or `{"rotate":true}` (body required) | `200 {"publisher":{"app_id","publisher_user_id","topic_pattern","created_at","updated_at"}}` (never the token) | 400, 401, 403, 404 `app has no approved binding`, 409 `publisher mint already in progress`, 429 `publisher mint rate limit exceeded`, 502, 503 |
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
| `POST /v1/apps/{app_id}/heartbeats`                          | `{"app_id"?,"name","period_seconds","grace_seconds","destinations":[…]?}` (`destinations` may be omitted, `null` or `[]`) | `201 {"check":{…}}`                    | 400 validation, 400 `app_id must be the app's own id`, 400 `unknown destination`, 409 `check already exists`, 401/403 (key), 503 `heartbeats unavailable`/`identity unavailable` |
| `GET /v1/apps/{app_id}/heartbeats`                           | none                                                               | `200 {"checks":[…]}` (only the app's own)    | 401/403 (key), 503                                                     |
| `GET`, `PATCH`, `DELETE /v1/apps/{app_id}/heartbeats/{id}`   | `PATCH`: any of `period_seconds`, `grace_seconds`, `destinations` (`[]` clears, `null` or omitted leaves unchanged) | `200` (`DELETE`: `{"id","deleted"}`)         | 400 invalid id; `PATCH` 400 validation / `app_id and name are immutable`; 404 `check not found`; 401/403 (key), 503 |
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

Message object (illustrative values; `status` is `pending`, `delivered`, `dead`, `partial`, `scheduled`,
`cancelled`, `unrouted` or `release_failed`; a delivery `state` is `pending`, `delivered`, `dead`, `scheduled` or `cancelled`):

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

Readiness body: `{"status":"ok","schema_version":14,"expected_schema_version":14,"last_migration_failure":null,"heartbeat_checker":{"last_tick_at":"<RFC3339>","stale":false},"heartbeat_prober":{"last_tick_at":"<RFC3339>","stale":false},"held":{"available":true,"stale":false,"as_of":"<RFC3339>","total":0,"messages":{"count":0,"oldest_held_at":null,"oldest_age_seconds":null},"unassigned_alerts":{"count":0},"apps":[],"apps_more":0}}`. The `held` block is informational (messages and heartbeat alerts waiting for a destination) and never changes the status.
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
| `POST /v1/admin/heartbeats`                          | `{"app_id","name","period_seconds","grace_seconds","destinations":["<name>",…]?}` (`destinations` may be omitted, `null` or `[]`) | `201 {"check":{…}}`                              | 400 validation or `unknown destination`, 409 `check already exists`          |
| `POST /v1/admin/heartbeats` (probe check)            | `{"app_id","name","destinations":[…]?,"probe":{"url","expected_statuses","body_match"?,"timeout_seconds","interval_seconds","access_token"?:{"client_id","client_secret"}}}` (no `period_seconds`/`grace_seconds`; `destinations` may be omitted, `null` or `[]`) | `201 {"check":{…}}` with a `probe` block | 400 validation (`invalid probe.<field>`), 400 if `period_seconds`/`grace_seconds` is present at all, 409 `check already exists` |
| `GET /v1/admin/heartbeats`                           | none                                                                                          | `200 {"checks":[…]}`                             |                                                                              |
| `GET`, `PATCH`, `DELETE /v1/admin/heartbeats/{id}`   | `PATCH`: any of `period_seconds`, `grace_seconds`, `destinations` (`[]` clears, `null` or omitted leaves unchanged) | `200 {"check":{…}}` (`DELETE`: `{"id","deleted"}`, idempotent) | 400 `app_id and name are immutable`, 404 `check not found`         |
| `PATCH /v1/admin/heartbeats/{id}` (probe check)      | `destinations` and/or `probe` with any of `url`, `expected_statuses`, `body_match` (`null` clears), `timeout_seconds`, `interval_seconds`, `access_token` (omitted keeps, `null` clears, a `{client_id,client_secret}` pair replaces) | `200 {"check":{…}}` | 400 validation, 400 `check kind is immutable` (`probe: null`), 400 on `period_seconds`/`grace_seconds`, 404 |
| `POST /v1/admin/heartbeats/{id}/pause` and `/resume` | none                                                                                          | `200 {"check":{…}}` (idempotent)                 | 404                                                                          |
| `POST /v1/admin/apps/{app_id}/ntfy-publisher`        | optional `{"rotate"?:bool}` (strict JSON; absent means `false`)                               | `200 {"publisher":{"app_id","publisher_user_id","topic_pattern","created_at","updated_at"}}` (never the token) | 404 `app has no approved binding`, 409 `publisher mint already in progress`, 502 mint failed, 503 not configured or 2 mints already in flight (operator mints are not rate limited) |
| `POST /v1/admin/provision`, `/deprovision`, `/reset-password` | the provisioning-api body (app_id, email or user id), relayed                         | provisioning-api's answer, as-is                 | 400 validation, 502 upstream failed, 503 not configured                      |
| `GET /v1/admin/users`; `DELETE /v1/admin/users/{user_id}` | none                                                                                | provisioning-api's answer, as-is                 | 400 empty `user_id`, 502, 503                                                |
| `POST /v1/admin/test-notify`                         | `{"app_id","recipients"?:[…],"channel","message","title"?}`                                   | provisioning-api's answer, as-is                 | 404 `app has no approved binding`, 400, 502, 503                             |
| `POST /v1/admin/apps/{app_id}/deprovision`           | `{ "app_id" }` repeating the path app_id (confirmation)                                       | `200 {"app_id","removed","unbound","deleted":{"checks","routes","destinations","orphaned_routes","orphaned_checks","held_messages"}}` (irreversible, idempotent) | 400 `invalid app_id` or missing/different body `app_id`, 409 mint in progress (retry), 502 provisioning-api teardown failed, 503 unavailable |
| `GET /v1/admin/apps/{app_id}/ntfy-publisher`         | none                                                                                          | `200 {"publisher":{…}}` (never the token)        | 404 when none is stored                                                      |
| `DELETE /v1/admin/apps/{app_id}/ntfy-publisher`      | none                                                                                          | `204` (idempotent)                               |                                                                              |
| `POST /v1/admin/messages`                            | the `POST /v1/messages` envelope, any `app_id` (optional `?wait=0..10`)                       | `202`, same as `POST /v1/messages`               | as `POST /v1/messages`, no key needed (admin send-test uses it)              |
| `GET /v1/admin/app-identity/requests`                | none                                                                                          | `200 {"requests":[…]}` (pending first, then rejected from the last 30 days); each carries `"kind":"app"\|"registrar"` and `"app_id": string\|null` (null for a registrar request) |                                                  |
| `POST /v1/admin/app-identity/requests/{id}/approve`  | none                                                                                          | `201 {"binding":{…}}` (the request is deleted)   | 400 `invalid request id`, 404 `request does not exist`, 409 `request is not pending` / `request is a registrar request` / `app id is reserved by a registrar` / `app already bound` / `workflow identity already bound` |
| `POST /v1/admin/app-identity/requests/{id}/approve-registrar` | `{"prefix":"hosted_"}` (strict JSON)                                                 | `201 {"registrar":{id, prefix, github_repository, github_repository_id, github_owner_id, workflow_path, git_ref, environment, approved_at, app_count, app_limit}}` (the request and its sibling pending requests are deleted; pending app requests inside the prefix are rejected) | 400 `invalid prefix` (grammar `^[a-z0-9][a-z0-9_]{0,30}_$`, any prefix starting with `platform_`, stem `everyone`), 404 `request does not exist`, 409 `request is not pending` / `request is not a registrar request` / `prefix already used` / `prefix overlaps an approved app id or registrar` / `workflow identity already bound` |
| `GET /v1/admin/app-identity/registrars`              | none                                                                                          | `200 {"registrars":[{id, prefix, github_repository, github_repository_id, github_owner_id, workflow_path, git_ref, environment, approved_at, app_count, app_limit}]}` sorted by prefix; `app_limit` is the per-registrar app cap (50 today) |  |
| `DELETE /v1/admin/app-identity/registrars/{id}`      | none                                                                                          | `200 {"id","deleted":bool,"unbound_apps":int}` (idempotent; unbinds every app the registrar created) | 400 `invalid registrar id` |
| `POST /v1/admin/app-identity/bindings/{app_id}/acknowledge` | none                                                                                   | `200 {"binding":{…}}` (idempotent; keeps the first time) | 400 `invalid app_id`, 404 `binding does not exist` |
| `POST /v1/admin/app-identity/bindings/acknowledge-all` | `{"app_ids":["…"]}` (strict JSON; 1-200 ids, the New rows the page displayed)               | `200 {"acknowledged":int}` (marks only the listed registrar apps still New; unknown, normal or already-seen ids are skipped; a repeat returns `0`) | 400 `app_ids must list 1 to 200 apps`, 400 `invalid app_id` |
| `POST /v1/admin/app-identity/requests/{id}/reject`   | none                                                                                          | `200 {"request":{…,"status":"rejected"}}` (idempotent) | 400, 404                                                              |
| `GET /v1/admin/app-identity/bindings`                | none                                                                                          | `200 {"bindings":[{app_id, github_repository, …, "registrar":{id, prefix}\|null, "acknowledged_at": string\|null, "keys":[{id, key_hint, state, …}]}]}`; `state` is `active`, `superseded` or `revoked` |  |
| `POST /v1/admin/app-identity/keys/{id}/revoke`       | none                                                                                          | `200 {"key":{…}}` (idempotent)                   | 400 `invalid key id`, 404 `key does not exist`                               |
| `DELETE /v1/admin/app-identity/bindings/{app_id}`    | none                                                                                          | `200 {"app_id","deleted":bool}` (idempotent; every key stops, owned objects stay) | 400 `invalid app_id`                      |

Approve precedence: `POST /v1/admin/app-identity/requests/{id}/approve` answers `404 request does not
exist`, `409 request is not pending`, `409 request is a registrar request` and the reserved-id `409`
before it checks that person-service is configured. `503` (app registry not configured) now appears only
for an approvable app request. On a deploy without person-service those cases answered `503` before.

Notes on the provisioning relay and publisher routes above (`POST /v1/admin/provision`,
`POST /v1/admin/deprovision`, `POST /v1/admin/reset-password`, `POST /v1/admin/test-notify`,
`GET /v1/admin/users`, `DELETE /v1/admin/users/{user_id}`):
- Operator reset-password is exempt from the door's reset limits (per app and per person).
- Operator provision and test-notify require an approved binding for the `app_id`
  (`404 "app has no approved binding"`).
- Publisher responses never carry a token.
- `POST /v1/admin/apps/{app_id}/deprovision` is the one-click, irreversible app teardown (see "Pausing and
  removing an app").

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
2678400; `grace_seconds` 60 to 604800; 0-20 distinct, existing `destinations`. A check view is
`{"id","app_id","name","period_seconds","grace_seconds","paused","state","last_ping_at","last_ping_kind","down_since","deadline_at","destinations":[{"name","type","enabled"}],"created_at","updated_at"}`
with `state` one of `new`, `up`, `late`, `down` and `last_ping_kind` `success` or `fail`.

**Probe checks (operator-created website checks).** Instead of a push schedule a probe check carries a
`probe` object; create and patch decode strictly, so an unknown key at any depth is a 400 `invalid JSON
body`. Limits: `url` at most 2048 bytes, lowercase `https://` prefix, no `#`, no userinfo, host not
`localhost`/`*.localhost`/an IP literal; `expected_statuses` 1 to 10 distinct integers, each 100 to 599;
`body_match` 1 to 256 bytes of valid UTF-8 without NUL (optional); `interval_seconds` 60 to 86400;
`timeout_seconds` 1 to 8 and strictly below the interval; each `access_token` half (`client_id`,
`client_secret`) 1 to 256 bytes of characters 0x21 to 0x7E. `access_token` is write-only: omitted on a
patch keeps the stored pair, `null` clears it, a pair replaces it, and a view returns only
`has_access_token`. A probe check's view adds a `probe` block
`{"url","expected_statuses","body_match","timeout_seconds","interval_seconds","consecutive_failures","failure_threshold","has_access_token","last_result":{"at","status","reason"}|null}`
(push checks have no `probe` key). `failure_threshold` is the number of consecutive failed probes that
takes the check down; read it from the view. `last_result.reason` is one of `http_status`, `timeout`,
`body_mismatch`, `blocked`, `tls`, `scheme`, `network` or `config` (and a down check's `down_reason` can
also be `missed` or `fail`). A host that resolves to a
private address is not rejected at save; it shows later as a `blocked` result. Probes run from the VPS,
so they cannot detect a VPS or Cloudflare Tunnel outage (the watchdog covers that).

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

An app integration involves two hosts:

| Host                              | What it is                                                                                                  | How you authenticate to it                                                                                                                                                         |
| --------------------------------- | ----------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `https://notifs-delivery.4irl.app` | **delivery-api**, the one door: your app backend provisions users, mints its publisher and sends messages here. | Your app's Cloudflare Access **service token** (`CF-Access-Client-Id` / `CF-Access-Client-Secret`) plus your delivery key (`Authorization: Bearer dak_…`).                          |
| `https://notifs.4irl.app`         | **ntfy server**: where your users receive notifications. Subscribers only; not behind Cloudflare Access.     | The subscriber's **ntfy token** (`Authorization: Bearer tk_…`) or their username and generated password.                                                                           |

Your backend talks only to `notifs-delivery.4irl.app`. The provisioning service behind it has no public
hostname and is reachable only through delivery-api, so there is nothing else to call or protect.

**Identity is the user's email.** The service hashes it into a stable, opaque `person_hash`
(16 chars). The same email is the same person across every 4IRL app, and you never send or store the
raw email in a topic. You do **not** compute the hash yourself; the API returns it.

**The credentials involved:**

| Credential                  | Where it comes from                                      | Who holds it                                         | Used to                                                        |
| --------------------------- | -------------------------------------------------------- | ---------------------------------------------------- | -------------------------------------------------------------- |
| **`dak_` app key**          | the `notifs-app` action's key exchange (see below)       | your app **backend** (a secret)                      | authenticate every call to delivery-api                        |
| **Subscriber token** `tk_…` | returned by provision, once per user                     | the user's **client** (or your backend, if you relay) | **receive** notifications (read-only on that user's topics)    |
| **Generated password**      | returned by provision, only on a person's first call     | shown to the user once, never stored                 | sign in to username-and-password ntfy clients (iOS)            |

There is **no** app-held publisher token: delivery-api keeps each app's ntfy publisher sealed and
publishes for you when you send a message.

**Topic naming** (one channel per app: `all`):

```
{app_id}-{person_hash}-all           ← one channel per app; delivery-api publishes here for you
{app_id}-{person_hash}-*             ← everything for one user (their subscriber token can read this)
{app_id}-broadcast                   ← the app-wide topic every provisioned user can read
```

Example: `urls4irl-v4sf4e5teivpe3zi-all`.

> **Tokens and passwords are shown once.** Provision reveals the subscriber token (and a first
> password) exactly once. There is no "fetch it again" endpoint; recover a lost password with
> reset-password, and a lost token by provisioning the user again.

---

## Prerequisites

Three things must exist before your backend can call the door:

1. **An Access service token authorized on the `notifs-delivery` application.** The operator creates it
   in Zero Trust → Access → Service Auth, adds it to the Service Auth policy on
   `notifs-delivery.4irl.app` and hands you the **Client ID** and **Client Secret** (shown once). Your
   backend stores both as secrets and sends them on every call:

   ```
   CF-Access-Client-Id: <client-id>
   CF-Access-Client-Secret: <client-secret>
   ```

   Without this, Cloudflare Access answers `403` or a redirect and you never reach the API.
2. **An approved app identity binding** for your `app_id`. See **Operator runbook: app identities**
   below: the first deploy files a request and the operator approves it once.
3. **A `dak_` key** from the `notifs-app` action, delivered to your backend as a secret. Each exchange
   supersedes the previous key, which keeps working for a 24 h grace period. See **App identity** above.

`app_id` rule: lowercase letters, digits, and underscores, 1–63 chars (e.g. `urls4irl`, `chores4irl`).

The `dak_` key is the credential for `POST /v1/apps/{app_id}/{provision,deprovision,reset-password,set-password,ntfy-publisher}`
as well as `/v1/messages` and heartbeats, so one key covers everything your app does.

---

## Provisioning your users through the door

Every call below goes to `https://notifs-delivery.4irl.app` with the Access headers and
`Authorization: Bearer dak_<43 chars>`. The `app_id` in the path must be the one your key belongs to
(otherwise `403 app_id does not match app key`).

<!-- Fenced json examples in this section and in "Subscriber login" carry a tag after json, written route.kind (route: provision, deprovision, set-password, reset-password, ntfy-publisher; kind: request, response, error). scripts/docs-onedoor-contract.test.mjs reads the tag and checks every key against that route's struct; an untagged or misspelled fence fails make scripts-test. -->

### Provision a user

Once per user, when they enable notifications. Send the verified email only:

```
POST https://notifs-delivery.4irl.app/v1/apps/urls4irl/provision
Content-Type: application/json
CF-Access-Client-Id: <access-client-id>
CF-Access-Client-Secret: <access-client-secret>
Authorization: Bearer dak_<43 chars>
```

```json provision.request
{ "email": "alice@example.com" }
```

Response:

```json provision.response
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

`password` is present only on the call that generated the person's first password. **Always display
it** (once; never store or log it) and see "Subscriber login — the generated password". The response
carries `Cache-Control: no-store`: never cache or log the body.

**Store, keyed to your user:**
- `person_hash`: you will want it to identify the user's topic. **Always store this.**
- `token` (subscriber token): store it if your backend relays notifications; otherwise deliver it to
  the user's client (see the next subsection). Either way, capture it now.

> Re-provisioning the *same* user issues a fresh token and invalidates the old one. Provision once per
> user and persist the result; don't call it on every login.

If a retry after an ambiguous failure (a timeout, a `502` or `503`) succeeds **without** a `password`,
the first attempt probably generated it and the response was lost. Recover with
`POST /v1/apps/{app_id}/reset-password`, after verifying the user owns the email (see the contract
note under "Subscriber login — the generated password").

### Receive notifications (subscriber side)

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

### Mint or rotate the publisher

delivery-api publishes recipient messages through a per-app ntfy publisher that it stores sealed. Your
app (or the operator) mints it once; you never see a token:

```
POST https://notifs-delivery.4irl.app/v1/apps/urls4irl/ntfy-publisher
Content-Type: application/json
CF-Access-Client-Id: <access-client-id>
CF-Access-Client-Secret: <access-client-secret>
Authorization: Bearer dak_<43 chars>
```

The body is required: send `{}` to mint, or `{"rotate": true}` to rotate. An empty body is a `400`.

```json ntfy-publisher.request
{ "rotate": true }
```

Response (no token is ever returned):

```json ntfy-publisher.response
{
  "publisher": {
    "app_id": "urls4irl",
    "publisher_user_id": "urls4irl-publisher",
    "topic_pattern": "urls4irl-*",
    "created_at": "2026-11-05T17:00:00Z",
    "updated_at": "2026-11-05T17:00:00Z"
  }
}
```

- At most **10 mints per app per hour**.
- `rotate` false or omitted adds a token and keeps the old ones valid; `rotate: true` revokes the old
  ones first, then mints the new one.
- Errors: `409 "publisher mint already in progress"`, `404 "app has no approved binding"`; the full
  list is in the per-route note under the errors table below.

Next: send notifications, see [Sending through delivery-api](#sending-through-delivery-api-post-v1messages).

### Deprovision a user

When the user disables notifications or leaves. Send exactly one of `email` or `person_hash`:

```
POST https://notifs-delivery.4irl.app/v1/apps/urls4irl/deprovision
Content-Type: application/json
CF-Access-Client-Id: <access-client-id>
CF-Access-Client-Secret: <access-client-secret>
Authorization: Bearer dak_<43 chars>
```

```json deprovision.request
{ "person_hash": "v4sf4e5teivpe3zi" }
```

Response:

```json deprovision.response
{ "user_id": "u_v4sf4e5teivpe3zi", "app_id": "urls4irl", "removed": true }
```

The call revokes this app's topic and broadcast grants and this app's tokens for the person. When the
person then holds no grant in any other app, their ntfy user (and password) is deleted too. Deprovision
is **not idempotent**; its retry behavior is exact:
- `200 {removed}` when the person's ntfy user is already gone, or when this app's grant is revoked by
  the call. That is why a repeat call answers `200` once the person held no other app.
- `404 "user does not exist"` when the person exists but holds no grant in this app. This includes a
  repeat call while the person still has another app. Treat a `404` as already removed.
- `removed: true` is also the answer for a person who never existed.
- A `404` also results from an email whose person exists only in another app (a person absent everywhere answers `200`), so deprovision with the `person_hash`
  returned by provision rather than a re-typed email.

### Door errors

| Status and body | Meaning | What to do |
| --- | --- | --- |
| `400 invalid JSON body` | Malformed JSON, trailing data, or any field not in the route's body (for example `person_hash` on provision). | Fix the request; do not retry. |
| `400 email required` | The `email` field is missing or blank. | Fix the request; do not retry. |
| `400 password required` | set-password only: the `password` field is missing. | Fix the request; do not retry. |
| `400 exactly one of email or person_hash required` | Deprovision needs exactly one identifier. | Fix the request; do not retry. |
| `400 invalid person_hash` | The `person_hash` is not a valid hash. | Fix the request; do not retry. |
| `401 app key required` / `401 invalid app key` (with `WWW-Authenticate: Bearer`) | The `dak_` key is missing, malformed, unknown or expired. | Fix the key; run the `notifs-app` key exchange again if it was superseded more than 24 h ago. Do not retry unchanged. |
| `403 app_id does not match app key` | The `app_id` in the path is not your key's app (an `app_id` in the body is an unknown field: `400 invalid JSON body`). | Fix the request; do not retry. |
| `404 user does not exist` | reset-password and set-password: the person is absent or has no grant in this app (deliberately indistinguishable). Deprovision: the person exists but holds no grant in this app (an absent person answers `200 {removed:true}`). | reset-password and set-password: stop, the person is not in this app. Deprovision: treat as already removed. |
| `413 request body too large` | The body exceeds the limit. | Fix the request; do not retry. |
| `429` + `Retry-After` | A rate limit was hit (reset-password limits or a provisioning-api limit). Every reset-password attempt counts toward the door limits (3 per app+person per hour), so retry sparingly rather than in a tight loop. | Wait for `Retry-After`, then retry with backoff. |
| `500` (relayed) | Provisioning-api failed internally. | Retry later; contact the operator if it persists. |
| `502 provisioning request failed` | Provisioning-api was unreachable or rejected delivery-api's internal credential. Never an app-level answer. | Retry later with backoff; contact the operator if it persists. After an ambiguous provision failure, see the password-recovery note under "Provision a user". Reset-password: retry sparingly, every attempt counts. |
| `503 provisioning unavailable` | Provisioning is not configured on this delivery-api. | Retry later with backoff; contact the operator if it persists. After an ambiguous provision failure, see the password-recovery note under "Provision a user". Reset-password: retry sparingly, every attempt counts. |
| `503 identity unavailable` | The app identity store is down or unconfigured. Distinct from `provisioning unavailable`. | Retry later with backoff; contact the operator if it persists. |
| `503 reset limit unavailable` | reset-password only: the limiter is unconfigured or its store is down. It fails closed; no reset was attempted. This is not a `429`. | Retry later with backoff, sparingly. |

The `503`s answered **before** the key is checked are exactly: `503 provisioning unavailable` (the
provisioning client is unconfigured), `503 identity unavailable` when the identity service is
unconfigured, and `503 reset limit unavailable` on reset-password (the limiter is unconfigured). The
`identity unavailable` `503` can also appear after key lookup (a store failure). The mint route's
`503 "publisher minting unavailable"` for an unset `DELIVERY_PROVISIONING_URL` is answered **after**
key validation.

Other provisioning-api `4xx`/`5xx` answers (for example `400` invalid email, `400` invalid password,
`500`) are relayed unchanged with their own error body; only upstream `401`/`403`/redirect answers
become `502`.

**Per-route note: `ntfy-publisher`.** The mint route's `429`, `502` and `503` differ from the shared
rows, which apply to the other four routes only:

| Status and body | What to do |
| --- | --- |
| `400` (the body is required) | Send `{}` or `{"rotate": true}`; fix the request, do not retry. |
| `401` | Fix the key; run the `notifs-app` key exchange again if the key was superseded. |
| `429 "publisher mint rate limit exceeded"` + `Retry-After` | Wait for `Retry-After`, then retry. |
| `409 "publisher mint already in progress"` | Another mint for this app is in progress; retry shortly. |
| `404 "app has no approved binding"` | Do not retry: the app's binding was removed while the call ran (for example teardown or unbind in progress), because a valid `dak_` key exists only for an approved binding. Contact the operator. |
| `502 "provisioning-api mint failed"` | Retry later with backoff; contact the operator if it persists. |
| `503 "publisher minting unavailable"` | Retry later with backoff; contact the operator if it persists. Also answered at the concurrent-mint cap, for an unset `DELIVERY_PROVISIONING_URL`, and for a store failure after the token was minted upstream. |

Rotate caveat: with `{"rotate": true}` the old tokens are revoked upstream before the new one is
stored, so after a `502` or `503` the app may have no working publisher until a mint succeeds. Retry
the rotate promptly.

Next: send notifications, see [Sending through delivery-api](#sending-through-delivery-api-post-v1messages).

---

## Broadcast / site-wide announcements

Each app has a single shared **broadcast topic** for site-wide announcements:
`{app_id}-broadcast` (e.g. `urls4irl-broadcast`).

**Receiving** works today. Every provisioned user is granted **read** on `{app_id}-broadcast`
automatically at provision, always on per app (no per-user opt-in or opt-out), so **no extra
registration** is needed. The topic name is returned as `broadcast_topic` in the provision response, so
your client can discover it instead of hardcoding it. Subscribe exactly like a personal topic, with the
shared name:

```
GET https://notifs.4irl.app/urls4irl-broadcast/json
Authorization: Bearer tk_<subscriber-token>
```

The grant is removed when the user is deprovisioned, so a departed user stops receiving broadcasts.

**Sending** a broadcast has no supported path through delivery-api today: there is no delivery-api
route for it and apps hold no publisher credential. This is tracked in
[4IRL/4irl-notifs#174](https://github.com/4IRL/4irl-notifs/issues/174). Until that lands, do not plan
site-wide announcements on this service.

---

## Sending through delivery-api (`POST /v1/messages`)

An app submits an envelope to delivery-api and lets it deliver
(retries, backoff and a delivery log included). The human-facing sinks and per-person recipients below
apply to that path.

delivery-api is reachable at `https://notifs-delivery.4irl.app`, behind Cloudflare Access. Send the same
`CF-Access-Client-Id` / `CF-Access-Client-Secret` headers as for every call to the door, using a service token the
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
| `to.via`          | no       | with `to.recipient` only: `ntfy` (default when absent or `null`), `personal` or `both`; case-sensitive, an empty string is `400` (see "Recipients" and "Choosing where a recipient message goes") |
| `to.personal_destination_ids` | no | with `to.recipient` only: ids of the person's personal destinations; required (1-50 unique positive integers) for `via` `personal` and `both`, refused for `ntfy`; `[]` counts as absent |
| `critical`        | no       | boolean, recorded only (see `critical` below)                                              |
| `payload`         | yes      | a single JSON object, at most 128 KiB, storable in Postgres jsonb                         |
| `display`         | no       | `title` (max 250 bytes), `body` (max 4096), `url` (max 2048, https), `priority` (`min` `low` `default` `high` `urgent`) |
| `deliver_at`      | no       | RFC3339 timestamp; a future time holds the message until then (see Deferred send below); absent, `null` or a past time sends at once |

Validation is checked in a fixed order (`app_id`, `channel`, `idempotency_key`, `to`, `deliver_at`,
`payload`, `display`) and the first failure is returned as `400 {"error":"<message>"}`, for example
`invalid app_id`, `invalid channel`, `invalid idempotency_key`, `invalid to.destination`,
`to.destination and to.recipient cannot both be set`, `to.recipient must be an email or person_hash`,
`to.via requires to.recipient` (also returned for `to.personal_destination_ids` without a recipient),
`invalid to.via`, `to.personal_destination_ids not allowed for to.via ntfy`,
`to.personal_destination_ids required for to.via`, `invalid to.personal_destination_ids` (non-positive,
duplicate or more than 50 ids), `invalid deliver_at`, `invalid payload`, `invalid display.title`, `invalid display.body`,
`invalid display.url`, `invalid display.priority`.

A successful submit is `202 {"message": {...}, "duplicate": false, "replaced": false}`. The message
carries `message_id`, `app_id`, `channel`, `idempotency_key`, `critical`, `status` (`pending`,
`delivered`, `dead`, `partial`, `scheduled`, `cancelled`, `unrouted` or `release_failed`), `deliver_at` (RFC3339 UTC as submitted, except that a replace which releases the message at once
reports the release time, or `null` when none was given), `created_at`, `cancelled_at` (RFC3339 UTC, or `null` unless cancelled) and `deliveries[]`
(`id`, `destination`, `state`, `dead_reason`, `attempt_count`, `last_status_code`, `next_attempt_at`,
`delivered_at`). A delivery `state` is `pending`, `delivered`, `dead`, `scheduled` or `cancelled`;
`next_attempt_at` is set only while the delivery is `pending`.

### Idempotency and `wait`

- **Idempotency:** `(app_id, idempotency_key)` identifies a message. Re-submitting it returns the
  original message with `"duplicate": true` and never waits, so retrying after a timeout is safe. The
  one exception is a message still held by `deliver_at`, which a re-post with different content
  **replaces** (see Deferred send below). `to.via` and `to.personal_destination_ids` count as content
  (the ids compare as a set, so `[15,12]` equals `[12,15]`): a changed routing replaces a still-scheduled
  message, and is ignored (a `duplicate`) for an immediate, released or cancelled one.
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
- Both return a message held for `deliver_at` with `status` `scheduled`, a message held for lack of a route
  with `status` `unrouted` (no `deliveries`), and a cancelled one with `status` `cancelled`; none of these
  statuses is ever mixed with another, because a message moves between states as a whole.

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
  | Message was `unrouted` (held; now cancelled)         | `true`      | `cancelled`     | the message id   |
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

### Unrouted messages (held)

By default a send that matches no route (and names no `to`) is refused with `422 no destination matched`.
When the operator turns holding on (`DELIVERY_HOLD_UNROUTED=1`; it is off by default), `POST /v1/messages`
accepts such a send instead and keeps it.

- **What you get:** `202` with `status` `unrouted`, an empty `deliveries` array and `"duplicate": false`.
  `?wait=` is ignored (an immediate `202`). Nothing is delivered while the message is held.
- **Which sends are held:** only a new message sent with an app key on `POST /v1/messages` that fans out
  through the routes. A route that matches but whose destinations are all disabled counts as no match and is
  held the same way (as is a route whose only destinations belong to another app). A named destination that is unknown (`400`) or disabled (`409 destination disabled`)
  is still refused, and the operator send-test (`POST /v1/admin/messages`), heartbeat alerts and a replace of
  a `scheduled` message never hold. There is no `hold` field in the envelope; one is `400 invalid JSON body`.
- **Replay:** re-posting the key of a held message returns the stored message with `"duplicate": true`,
  content ignored, and does not add to the held count. `release_failed` (a held message whose release could
  not build its deliveries) is terminal for its key: re-posting the key returns it unchanged, so send the
  content under a new key.
- **Release:** once a matching route reaches an enabled destination, a held message is sent automatically,
  oldest first (a loop checks every 5 seconds). If it has a `deliver_at` still in the future, it is then
  scheduled for that time. `release_failed` (status) means it could never be delivered as written, for
  example missing display text for a human-facing destination.
- **Cancel:** `DELETE /v1/messages` (by id or by key) cancels an unscheduled held message: `200` with
  `"cancelled": true` and `"status": "cancelled"`. A cancelled message stays cancelled.
- **Caps and expiry:** an app may hold at most 1000 messages (`DELIVERY_MAX_HELD_PER_APP`, 1 to 100000):
  one more is `429 {"error":"too many held messages"}` and nothing is stored. All apps together may hold at
  most 100000 (`DELIVERY_MAX_HELD_TOTAL`): one more is `429 {"error":"held message capacity reached"}`; a
  replay is never refused by either. A held message that is not released within 7 days
  (`DELIVERY_HELD_TTL_DAYS`, 1 to 365) is removed by the retention sweeper.
- **Do not enable yet:** holding needs migration 0012. Do not turn the flag on before the release feature that
  turns held messages into deliveries is deployed, because until then nothing releases them.
- **Check it:** `onboard verify` reports a channel with no route as `unrouted` (and cancels its held test
  message) instead of a generic failure.

### Errors

| Status | Body `error`                                                                 | Meaning                                                    |
| ------ | ---------------------------------------------------------------------------- | ---------------------------------------------------------- |
| 400    | a validation message above, `invalid JSON body`, `invalid wait`, `unknown destination` | Fix the request                                  |
| 401    | `app key required`, `invalid app key`                                        | Missing or malformed `Authorization: Bearer dak_...`, or an unknown, revoked, unbound or past-grace key |
| 403    | `app_id does not match app key`                                              | The `app_id` names another app than the key's              |
| 409    | `destination disabled`                                                       | `to.destination` is disabled                               |
| 413    | `request body too large`                                                     | Body over 256 KiB                                          |
| 422    | `no destination matched`, `app has no ntfy publisher`, `destination type not yet supported`, `personal destination not available for recipient` | No route/destination/publisher can take it. With holding on, an app-key send that matches no route is `202 unrouted` instead (see Unrouted messages), but the operator send-test still gets `no destination matched` |
| 429    | `too many held messages`                                                     | The app already holds the maximum held messages (see Unrouted messages) |
| 429    | `held message capacity reached`                                              | All apps together hold the maximum held messages (see Unrouted messages) |
| 429    | `too many scheduled messages`                                                | The app already holds the maximum scheduled messages (see Deferred send) |
| 429    | `scheduled message capacity reached`                                         | All apps together hold the maximum scheduled messages (see Deferred send) |
| 503    | `messages unavailable`, `identity unavailable`                               | delivery-api is up but cannot accept messages, or cannot check the key (fails closed) |
| 500    | `internal error`                                                             | Retry later; the cause is logged server-side               |

Through Cloudflare Access an unauthorized token gets a Cloudflare `403`/redirect before reaching the API.

### Recipients

`to.recipient` takes an **email or a `person_hash`** and sends the notification to that person's
personal topic. Only the hash is stored (an email is hashed on submit and never persisted). The topic
is `{app_id}-{person_hash}-{channel}`, the same topic the user is subscribed to after provisioning
(see "Provision a user"). Because ntfy caps topics at 64 characters, `app_id` + `channel` must keep the whole topic
within 64 characters (a longer combination is rejected at submit). The app also needs a **publisher**
(minted by the app with `POST /v1/apps/{app_id}/ntfy-publisher` or by the operator with
`POST /v1/admin/apps/{app_id}/ntfy-publisher`, see the runbook below); with none, the submit fails closed with **422**.
The topic, publisher and topic-length requirements apply only when the message uses the ntfy leg
(`to.via` absent, `ntfy` or `both`); with `to.via` `personal` they do not apply (see "Choosing where a
recipient message goes").

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

### Personal destinations (a person's own Discord or webhook endpoints)

A person can receive an app's `to.recipient` messages at their **own** Discord or webhook endpoints,
instead of or in addition to their ntfy topic, as the app chooses per message (`to.via`). The app builds a "my endpoints" settings page and calls the six endpoints
below with its own `dak_` key; the service stores only the `person_hash` (the app asserts the email, it is
hashed and never stored, logged or returned). Types are `discord` or `webhook` only (anything else is
`400 type must be discord or webhook`); the person's ntfy topic is the separate `ntfy` leg, chosen with `to.via`.

**Calls** (all under `https://notifs-delivery.4irl.app/v1/apps/{app_id}/personal-destinations`, same
Access headers and `Authorization: Bearer dak_...` as the rest of the app door; `{app_id}` must be the key's
own app). **The person always goes in the JSON body, never the URL**: every body carries exactly one of
`email` or `person_hash` (both or neither is `400 exactly one of email or person_hash required`), and an
unknown body field is `400`. Responses are `no-store`.

| Call | Body | Success |
|---|---|---|
| `POST …/personal-destinations` | `{email\|person_hash, label, type, config, secrets, opt_in?}` | `201 {"personal_destination": {…}}` |
| `POST …/personal-destinations/lookup` | `{email\|person_hash}` | `200 {"person_hash": "…", "personal_destinations": [{…}]}` (newest first, empty list when none) |
| `PATCH …/personal-destinations/{id}` | `{email\|person_hash, label?, config?, secrets?, enabled?}` | `200 {"personal_destination": {…}}` |
| `DELETE …/personal-destinations/{id}` | `{email\|person_hash}` | `200 {"id": N, "deleted": true}` |
| `PUT …/personal-destinations/{id}/opt-in` | `{email\|person_hash}` | `200 {"personal_destination": {…}}` (idempotent) |
| `DELETE …/personal-destinations/{id}/opt-in` | `{email\|person_hash}` | `200 {"personal_destination": {…}}` (idempotent), or `200 {"id": N, "deleted": true}` the opt-out reaped the row (see "Opt-out" below) |

Both `DELETE` calls need a **JSON body** (the person selector); an empty body answers the generic
`400 invalid JSON body`. Set `Content-Type: application/json` and send at least `{"email": "…"}`.

Create example (the destination is opted in for the registering app unless `"opt_in": false`):

```
POST /v1/apps/urls4irl/personal-destinations
{
  "email": "user@example.com",
  "label": "My Discord",
  "type": "discord",
  "config": {},
  "secrets": { "webhook_url": "https://discord.com/api/webhooks/…" }
}
```

`config` and `secrets` follow the same per-type rules (and the same SSRF check on the URL, `400` when
refused) as operator and app destinations. `PATCH` merges `secrets` per key and they are write-only: every
response returns them masked.

**What each view contains** (every personal destination in a response): `id`, `person_hash`, `label`,
`type`, `config`, `secrets` (masked), `enabled`, `disabled_by` (`app`, `operator`, `system` or null),
`disabled_reason`, `registered_by_app_id`, `managed_by_caller` (you registered it and your app is still
there), `opted_in` (for the **calling app only**; other apps' opt-ins are never shown), `created_at`,
`updated_at`.

**Rules**
- **Visibility:** any app can `lookup` all of a person's personal destinations (masked). Only the
  registering app can `PATCH` or `DELETE` one (`403 personal destination is managed by another app`,
  also when the registering app has since been removed). Every app toggles the opt-in for **itself** only.
- **A different person or an unknown id is `404 personal destination does not exist`**, identical to an
  absent row (no oracle); a malformed id is the same `404`.
- **Cap:** at most 1000 personal destinations per person per registering app; each app has its own allowance
  for the same person (`409 personal destination limit reached`).
- **Operator disable sticks:** `PATCH {"enabled": true}` on a row the operator disabled is
  `409 personal destination disabled by the operator` and nothing in that body is written. A row the
  service disabled after the endpoint answered `410 Gone` (`disabled_by: "system"`) can be re-enabled by the
  registering app. `PATCH {"enabled": false}` on an already-disabled row is a no-op that keeps the
  existing `disabled_by`/`disabled_reason`.
- **Opt-out:** `DELETE …/opt-in` withdraws only your app. When the registering app was removed and your
  opt-out leaves the row with no opt-ins, the row is deleted in the same call and the answer is
  `{"id": N, "deleted": true}`; repeating the call is then `404`. An opt-out on a row whose registrar is
  still present never deletes it.
- **Errors:** `503 personal destinations unavailable` when the feature is not configured, `503` when the
  URL checker is unavailable (create and `PATCH` with a URL), `400` for validation, `500 internal error`
  otherwise.
- **Deprovisioning:** `POST …/deprovision` removes your app's opt-ins for the person and deletes the
  destinations you registered that no other app uses locally first, then relays; a cleanup failure answers
  `503 personal destination cleanup failed` and the relay is not sent (retry the call). Removing an app
  does the same for all its people. Because that cleanup runs before the relay, a deprovision whose relay
  fails (`502`) may already have removed the personal destinations; retrying the call finishes both.
- **Opt-in trust boundary:** any authenticated app that knows a person's email (or `person_hash`) can opt
  itself in to that person's personal destinations, by design. There is no per-person consent step, so only
  opt a person in after you have verified their email and their wish to receive messages there.

### Choosing where a recipient message goes (`to.via`)

There is **no implicit fan-out**: a recipient message never reaches a personal destination unless the app
names it. The app chooses per message with two sibling fields of `to.recipient`:

| `to.via` | Delivered to |
|---|---|
| absent or `null`, or `ntfy` | the person's ntfy topic only (`@recipient`); personal destinations are never read |
| `personal` | exactly the destinations in `to.personal_destination_ids`; **no ntfy delivery**, and no publisher or topic-length requirement |
| `both` | the ntfy topic plus exactly the destinations in `to.personal_destination_ids` |

```
{"to": {"recipient": "user@example.com", "via": "personal", "personal_destination_ids": [12, 15]}}
```

- **The app names the ids.** Get them from the `lookup` (or create) call above; there is no "all my
  destinations" shortcut. `personal` and `both` require 1-50 unique positive ids (the 50 bounds one send,
  because every id is its own delivery and outbound request; it is separate from the 1000-per-person
  stored cap); `ntfy` refuses ids; ids or `via` without `to.recipient` are refused. Those shape errors are
  `400` (messages in the Envelope contract list). An unknown `via` (any other value, wrong case or an empty
  string) is `400 invalid to.via`.
- **Every named id must be deliverable or the whole send is refused.** Each id must belong to the
  recipient, be enabled and be opted in for the sending app. Any miss answers
  `422 {"error":"personal destination not available for recipient"}` before anything is stored; the
  response never says which id or why (missing, disabled, other person, not opted in), and no id is
  silently skipped. A named destination whose type has no sink configured in this deployment gets the same
  `422`, so the response confirms nothing about an id.
- **Snapshot frozen at submit.** The set of targets is computed when the message is submitted (or a
  scheduled message is replaced) and never recomputed at release: a destination disabled or deleted
  afterwards fails that delivery (see the `dead_reason`s below), one added afterwards never receives it.
- **`via=personal` has no ntfy delivery.** Do not infer that the person got an ntfy push from the message
  status; the `deliveries[]` rows show exactly what was targeted.
- **Not echoed back.** The `202` message view does not repeat `via` or the ids.

**Routing per notification type (app side).** The app, not delivery-api, stores each user's choice, and
passes it on every send; delivery-api keeps no per-user routing preference. For example the app keeps
`{"new_url": {"via": "personal", "ids": [12]}, "account_alert": {"via": "both", "ids": [15]}}` per user and
builds the envelope from that row:

```
POST /v1/messages
{
  "app_id": "urls4irl",
  "to": { "recipient": "user@example.com", "via": "personal", "personal_destination_ids": [12] },
  "channel": "all",
  "idempotency_key": "new-url-9001",
  "payload": {},
  "display": { "title": "New URL", "body": "Someone added a URL" }
}
```

A notification type with no stored choice sends with no `via`, which is ntfy only.

**What you see in `deliveries[]`.** With `via` `ntfy` or `both` the message shows the ntfy `@recipient`
row; with `personal` it does not. Each named destination adds one row, named `@personal_<id>` (the
destination `id`), so `deliveries[]` can hold several
entries; a `202` is still not delivery. `?wait=` now waits for every one of them to have a first attempt, so
a slow personal endpoint delays the response (bounded by the 10 s clamp; read the states, a short wait is
not a failure). A personal delivery goes dead, never sent, with these `dead_reason`s:

| `dead_reason` | Meaning |
|---|---|
| `opted_out` | The opt-in was withdrawn after the message was submitted |
| `destination_disabled` | The personal destination is disabled (by the app, the operator, or the system after a `410`) |
| `destination_deleted` | The destination was deleted (or no longer belongs to that person) |
| `gone` | The endpoint answered `410 Gone`; the destination is also disabled (`disabled_by: "system"`) |
| `unsupported_type` | No sink for its type is wired |
| `terminal_status` / `budget_exhausted` / `blocked_address` | As for any destination (SSRF-blocked address, rejected, or retries exhausted) |

The operator's admin message views (scheduled list) show an empty `destinations` for recipient messages;
that is expected. The operator sees, disables, enables and deletes personal destinations in the admin UI
(Personal destinations section) and marks personal rows in the Delivery Log.

**Suggested settings-page flow**
1. **Lookup** on page load: `POST …/lookup {"email": "<signed-in user>"}`.
2. **List with a toggle:** show each `label`/`type` with a switch bound to `opted_in`. Switching on calls
   `PUT …/{id}/opt-in`, off calls `DELETE …/{id}/opt-in` (with the body). Only offer edit and delete when
   `managed_by_caller` is true. Show a disabled badge with `disabled_reason` when `enabled` is false, and
   hide the re-enable control when `disabled_by` is `operator`.
3. **Add:** a form posting `POST …/personal-destinations`; surface `409 personal destination limit reached`
   and the `400` text to the user.

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
through the normal message pipeline to the check's own destinations (0 to 20 of them, set when the
operator creates the check), as `channel: "heartbeat"` messages.

A check may have no destination. Its alert stays pending, is visible as an unassigned alert in the admin UI
and the watchdog, and is sent when a deliverable destination (enabled, supported type) is assigned. Manifests
are declarative: a heartbeat that omits `destinations` or lists `[]` clears any destinations the operator set
in the UI on the next apply.
Only the latest state is sent: a check has a single pending-alert slot, so one that went down and recovered
while unassigned sends only `recovered`.

**The check is created first**, by your manifest (`heartbeats`, applied by your deploy through
`/v1/apps/{app_id}/heartbeats`) or by the operator (Heartbeats section of the Delivery tab, or
`POST /v1/admin/heartbeats` with the proxy token), with your `app_id`, a `name`, a `period_seconds`, a
`grace_seconds` and its destinations. A ping for a check that does not exist is a `404`; pings never
create one.

**Probe checks are operator-created.** An operator can also add a website probe check, which delivery-api
runs itself. A push ping or `/fail` on a probe check returns `409` `check is a probe check`, and an app
cannot create a probe check (a `probe` key on `POST /v1/apps/{app_id}/heartbeats` is a 400). The probe
fields and limits are under "Heartbeat bounds" in the agent reference.

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
  token. Apps hold no publisher token of their own, so rotating affects only delivery-api's copy.
- An admin-UI "remint with rotate" on the same app revokes delivery-api's token too. Recipient
  deliveries then dead-letter as `terminal_status` with a 401 until the publisher is re-minted.
- After `POST /v1/admin/apps/{app_id}/deprovision` the app's ntfy publisher user is gone, but delivery-api still holds
  its stored token, so recipient submits are accepted (202) and then dead-letter on a 401. Remove the
  stored publisher so submits that use the ntfy leg fail closed (422, `app has no ntfy publisher`):
  `curl -X DELETE https://notifs-delivery.4irl.app/v1/admin/apps/<app>/ntfy-publisher` with the same two
  proxy-token headers (204, idempotent; it removes only delivery-api's copy and revokes nothing in ntfy).
  Re-mint the publisher before recipient sends are expected to work again. (The app cascade
  `POST /v1/admin/apps/{app_id}/deprovision` already deletes the stored publisher for you.)
- An app can also mint its own publisher with `POST /v1/apps/{app_id}/ntfy-publisher` (see "Mint or rotate
  the publisher"). Self-mint is limited to 10 per hour per app and at most 2 concurrent mints across all
  apps; past the concurrency cap the call answers `503`.

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

There is no separate provisioning hostname any more: provisioning is reachable only through
delivery-api (the app door on this same host, and the `/v1/admin` relay for you), so no extra Access
application, tunnel hostname or DNS record is needed for it.

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

**Approve a registrar (once per deploy workflow; model: "Registrars: one workflow, many apps").**
1. When a trusted deploy workflow's first bare-name exchange fails with "registrar request #N awaits operator
   approval", open **Requests**. The registrar row shows no app id, only the GitHub-verified repository,
   workflow file, ref and environment. Approve only a workflow you trust to create apps: from now on it
   creates and keys any number of apps (up to 50) inside the prefix without asking.
2. **Approve as registrar...** and choose the **prefix**: lowercase letters, digits and underscores, ending in
   `_`, 2-32 characters (for example `hosted_`). Pick something that names the platform, since every app it
   creates is `prefix + name`. `platform_*` is refused on purpose: `_platform.json` shared destinations use
   the `platform_` namespace and onboarding rejects any app id starting with it, so a prefix like
   `platform_eu_` would mint apps that onboarding refuses (`400 invalid prefix`; `everyone_` and the
   reserved ids are refused too). A prefix that overlaps a bound app id or another registrar prefix is
   `409 prefix overlaps an approved app id or registrar`; a reused one is `409 prefix already used`.
   Approving also rejects every pending app request whose id now falls inside the prefix.
3. Apps the registrar creates appear under **Approved apps** with a `via <prefix>` marker and a **New**
   badge. They need no action. **Mark seen** clears one; **Mark all seen** clears only the New apps listed on
   the page (`POST bindings/acknowledge-all` with those ids), so an app created after the page loaded stays
   New until you look at it.
4. **Delete a registrar** (confirm; the dialog states the app count) removes the registrar and unbinds every
   app it created, so all of their keys stop at once; owned objects stay, as for Unbind. **Unbind** on a single
   registrar app is not a block: the registrar recreates it on its next deploy.

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

The subscriber token (from "Provision a user" and "Receive notifications") is what a **programmatic or
Android** client uses to subscribe. **iOS is different:** the ntfy iOS app's subscribe screen has **no
token field**, so an iOS user signs into the ntfy app with a **username + password**. The username is the
derived ntfy user id `u_<person_hash>` (the `user_id` from provision). The password is **generated by the
notifications service**, never chosen by the person or your app: a typeable `xxxx-xxxx-xxxx-xxxx` code
(about 80 bits). The shared setup page also asks Android users for this username and password, so every
person you link to it needs one.

### Getting the password

`POST /v1/apps/{app_id}/provision` returns `password` in its response **only** when that call generated
one (the first time a person is provisioned):

```json provision.response
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
  (every `200` from provision and reset-password carries it). It is not retrievable later.
- **Always display a returned password.** A person who existed before this change has no stored date and
  is **rotated** on their next provision: the returned password is the only delivery of the new one.
- **No `password` in the response** is normal (the person already has one, or the person store was
  unavailable). Do not treat it as an error; offer "Generate a new password" instead.
- **Recovery.** If a first-time provision's password never reached the person (for example the response
  was lost after the service recorded it), call `POST /v1/apps/{app_id}/reset-password` to get a new one.

The person then logs into the ntfy iOS app with **username `u_v4sf4e5teivpe3zi` + that password** (after
setting the Default server to `https://notifs.4irl.app`; the setup page below walks them through it), and
their existing read ACL lets them subscribe to their own `{app_id}-{person_hash}-*` topics and
`{app_id}-broadcast`.

> **Contract note: email ownership is the app's job.** notifs cannot verify that an email belongs to the
> signed-in user. A person is one ntfy user with one password across all apps, keyed by the email hash, so
> an app must only provision, reset or set a password for an email it has **verified**: lax verification
> in one app affects the person's other apps. The door does enforce that reset-password and set-password
> refuse (`404`) a person with no grant in the calling app, and that deprovision refuses (`404`) a person
> who exists but holds no grant in it; an absent person answers `200` with `removed: true`.

### `POST /v1/apps/{app_id}/reset-password` — generate a new password

```
POST https://notifs-delivery.4irl.app/v1/apps/urls4irl/reset-password
Content-Type: application/json
CF-Access-Client-Id: <access-client-id>
CF-Access-Client-Secret: <access-client-secret>
Authorization: Bearer dak_<43 chars>
```

```json reset-password.request
{ "email": "alice@example.com" }
```

```json reset-password.response
{ "user_id": "u_v4sf4e5teivpe3zi", "app_id": "urls4irl", "password": "k7mq-x2pw-9rnd-ft4h" }
```

| Status | Body | Notes |
| ------ | ---- | ----- |
| `200` | `{ "user_id", "app_id", "password" }` | The new password, shown once. `Cache-Control: no-store`. |
| `400` | `{ "error": "invalid JSON body" }` / `"email required"`; a malformed address is relayed from provisioning-api as `"invalid email"` | Fix the request. |
| `404` | `{ "error": "user does not exist" }` | The person is absent or has no grant in this app. |
| `429` | `{ "error": "reset rate limit exceeded" }` + `Retry-After` | A door limit: 30 per app per hour, 3 per app+person per hour. Every attempt that passes body validation counts, whatever its outcome. |
| `429` | `{ "error": "too many password changes" }` + `Retry-After` | provisioning-api's own per-person limit, a different layer. |
| `503` | `{ "error": "reset limit unavailable" }` | The limit store is down. It fails closed: no reset was attempted. |
| `500` / `502` / `503` | see "Door errors" | Retry later, sparingly. |

Every response from this endpoint carries `Cache-Control: no-store`.

- **Two door limits: 30 resets per app per hour and 3 per app+person per hour.** Every attempt that
  passes body validation counts toward both, including ones that end in `404` or an error, so retry sparingly, never in a tight loop.
- **A limit-store outage is a `503`, not a `429`.** If the door cannot check its limits it refuses with
  `503 "reset limit unavailable"` and attempts no reset; handle it as a retry-later outage.
- **provisioning-api has its own per-person limit** (`429 "too many password changes"`), applied after the
  door limits pass. A `429` from either layer carries `Retry-After`: wait that many seconds.
- **Warn first.** A reset replaces any earlier password (including one the person is already using on
  another device), so say so before the call, as the admin UI does: its Users list has a "Reset password"
  row action that asks for confirmation first, then reveals the new password once. Operators can use it
  instead of calling this endpoint by hand; it is subject to the same limit.
- The password is applied to the person, not to one app: resetting from any app changes the one ntfy
  password. That is why email ownership is the app's job (see the contract note above).

### Suggested UI copy

Use this copy so apps do not each invent their own. It is a suggestion: reword it as long as it says the
password is shown once and that it replaces the old one.

| Where | Text |
| ----- | ---- |
| Reveal lead | "This password is shown only once. It replaces any earlier password for this person." (name your own subject) |
| No-password state | "No password was returned by this request." plus a pointer: "call POST /v1/apps/{app_id}/reset-password from the app that serves them" |
| Warn-first, before a reset | "Generating a new password replaces the current one." |
| `429` wait message | "Too many password changes. Try again in `<Retry-After>` s." (the abbreviation reads correctly for any count, avoiding "1 seconds") |
| `503` message | "Password reset is temporarily unavailable. Try again later." |

Accessibility:
- Announce a late-arriving result through a **single persistent `role="status"` region**: one region,
  rendered before the content, with `aria-atomic="false"`, and a fresh node for each later message such as
  "Copied", so the reveal lead is not re-read.
- Show the password in a **labelled read-only field**, not inside a live region.
- Do not move focus programmatically on reveal.

### `POST /v1/apps/{app_id}/set-password` — not for new apps

`set-password` still exists for apps that let a person choose their own password, but **new apps should
use the generated password**. The person's one password is shared across every app they use. It records
that the person has a password (`password_set_at`) before changing it, so a later provision will not
replace a chosen password. It returns `500` and changes nothing when that record cannot be written (person
store unavailable). The password policy is 8–128 characters (`400 { "error": "invalid password" }`), and
the person must already be provisioned in this app (`404 { "error": "user does not exist" }`).

```
POST https://notifs-delivery.4irl.app/v1/apps/urls4irl/set-password
Content-Type: application/json
CF-Access-Client-Id: <access-client-id>
CF-Access-Client-Secret: <access-client-secret>
Authorization: Bearer dak_<43 chars>
```

```json set-password.request
{ "email": "alice@example.com", "password": "<chosen password, 8-128 chars>" }
```

```json set-password.response
{ "user_id": "u_v4sf4e5teivpe3zi", "app_id": "urls4irl", "updated": true }
```

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
https://notifs-setup.4irl.app/?app=chores4irl&user=u_v4sf4e5teivpe3zi&name=Chores4irl
```

| Param  | Required | Rule                                                                                                                                                                                                                     |
| ------ | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `app`  | yes      | Your `app_id`: `^[a-z0-9][a-z0-9_]{0,62}$`, and not the reserved word `everyone`.                                                                                                                                        |
| `user` | yes      | The `user_id` from `POST /v1/apps/{app_id}/provision`: `^u_[a-z2-7]{16}$`. This is the user's ntfy **username**.                                                                                                                       |
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
- Pass the `user_id` from `POST /v1/apps/{app_id}/provision` as the `user` parameter (it is the ntfy username), so
  the username the page shows is the one your user logs in with.
- The page requires JavaScript.

---

## Usage recommendations (this deployment)

The provisioning calls above are the mechanics; this is how to use them well against `notifs.4irl.app`.

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

**Periodic / bulk sends (e.g. hourly digests).** Send through `POST /v1/messages` (see "Sending through
delivery-api"); the service publishes to ntfy for you. Mind these points:
- Give every send a stable `idempotency_key` (for example `digest-<user>-<hour>`) so a retried job can
  never double-send, and use `deliver_at` to schedule a send instead of sleeping in your own job.
- Each **iOS-bound** message also costs one upstream poll to ntfy.sh (rate-limited) — the operator sets
  `upstream-access-token`.
- **Coalesce** many-per-user notifications into a single **digest** message (one `to.recipient` message
  with a digest title) instead of N separate ones — fewer requests and better UX.

**Missed-message catch-up.** The server retains messages for **24h**. When a client reconnects after
downtime, backfill with `since=`:
`GET https://notifs.4irl.app/{topic}/json?since=<id|duration|timestamp>` (up to 24h back). Don't rely on
a live stream alone to guarantee delivery across reconnects.

**Notification types & user preferences.** Model each notif type as a `Title` / `Tags` on the
single `{app_id}-{person_hash}-all` channel. Enforce user preferences at **publish time** — simply
don't emit a type the user disabled — so a broad subscriber token can't be used to receive muted
types.

---

## API reference — app door (`notifs-delivery.4irl.app`)

Every call needs the two `CF-Access-Client-*` headers and `Authorization: Bearer dak_…`. All bodies and
responses are JSON. The `app_id` in the path must be your key's app.

| Method & path                                | Auth                  | Request                                   | Success                                                                              | Errors                         | Rate limit |
| -------------------------------------------- | --------------------- | ----------------------------------------- | ------------------------------------------------------------------------------------ | ------------------------------ | ---------- |
| `POST /v1/apps/{app_id}/provision`           | Access token + `dak_` | `{ "email" }`                             | `{ user_id, app_id, person_hash, topic_pattern, broadcast_topic, token, password? }` | see "Door errors"              | provisioning-api limits (`429`) |
| `POST /v1/apps/{app_id}/reset-password`      | Access token + `dak_` | `{ "email" }`                             | `{ user_id, app_id, password }`                                                      | see "Door errors"              | 3 per app+person and 30 per app, per hour |
| `POST /v1/apps/{app_id}/set-password`        | Access token + `dak_` | `{ "email", "password" }` (not for new apps) | `{ user_id, app_id, updated }`                                                    | see "Door errors"              | provisioning-api limits (`429`) |
| `POST /v1/apps/{app_id}/deprovision`         | Access token + `dak_` | `{ "email" }` **or** `{ "person_hash" }`  | `{ user_id, app_id, removed }`                                                       | see "Door errors"              | provisioning-api limits (`429`) |
| `POST /v1/apps/{app_id}/ntfy-publisher`      | Access token + `dak_` | `{}` or `{ "rotate": true }` (required)   | `{ publisher: { app_id, publisher_user_id, topic_pattern, created_at, updated_at } }` | see "Door errors", per-route note | 10 per hour per app; at most 2 concurrent mints (`503`) |

Notes:
- `user_id` is always the derived ntfy id `u_<person_hash>`. `email` must be a valid address
  (validated after trim + lowercase — case-insensitive; `Alice@x.com` == `alice@x.com`).
- Errors are `{ "error": "<message>" }` with the matching HTTP status; see "Door errors" for every
  status, what it means and what to do.
- Every response from these routes carries `Cache-Control: no-store`.
- `POST /v1/apps/{app_id}/provision` returns `password` (`xxxx-xxxx-xxxx-xxxx`) **only** when this call
  generated one. See "Subscriber login — the generated password".
- Deprovision answers `200 {removed}` or `404` by the exact rule in "Deprovision a user"; callers treat a
  `404` as already removed. Do not call the route idempotent.
- `set-password` is legacy: new apps use the generated password and reset-password.
- `GET /healthz/provisioning` reports provisioning-api reachability only; it is for operators and the
  watchdog, not for apps.
- Operator-only management (cross-app) lives under `/v1/admin`: `/v1/admin/provision`,
  `/v1/admin/deprovision`, `/v1/admin/reset-password`, `/v1/admin/users`, `/v1/admin/users/{user_id}`,
  `/v1/admin/test-notify`, `/v1/admin/apps/{app_id}/deprovision` and
  `/v1/admin/apps/{app_id}/ntfy-publisher`, plus `GET /healthz/provisioning`. Apps do not call them.

## ntfy reference — `notifs.4irl.app`

- **Subscribe:** `GET https://notifs.4irl.app/{topic}/json` (or `/sse`, or WebSocket) with
  `Authorization: Bearer <subscriber-token>`.
- Apps do not publish to ntfy: send through delivery-api (see **Sending through delivery-api**).
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

### Provisioning users through the door

- [ ] Backend stores the Access Client ID and Secret and the `dak_` key as secrets.
- [ ] Access token and `dak_` secret wired (every door call sends both).
- [ ] Minted the app's ntfy publisher once with `POST /v1/apps/{app_id}/ntfy-publisher`.
- [ ] Email verified before any provisioning call.
- [ ] Never log or cache provision or reset-password request or response bodies.
- [ ] Reset flow handles `429` and `Retry-After`.
- [ ] On user opt-in, with a verified email: call `POST /v1/apps/{app_id}/provision`; store
      **`person_hash`** (+ subscriber token or deliver it to the client). Once per user, not per login.
- [ ] Send notifications through delivery-api (`to.recipient`), with a `Title`.
- [ ] User's client subscribes to `{app_id}-{person_hash}-all` with the subscriber token.
- [ ] Clients subscribe to `{app_id}-broadcast` (read grant added automatically at provision, returned as
      `broadcast_topic`). Sending broadcasts is not available yet (#174).
- [ ] On opt-out / account deletion: call `POST /v1/apps/{app_id}/deprovision` with the `person_hash`.
- [ ] Always display a `password` returned by provision (shown once, never stored or logged,
      `Cache-Control: no-store` honoured), and offer "Generate a new password" via
      `POST /v1/apps/{app_id}/reset-password` with the warn-first line and the `429` wait message (see
      "Subscriber login — the generated password").
- [ ] Once the person has their password (iPhone and Android users both type it into the ntfy app), link
      them to the shared setup page (see "Link your users to the setup page"); do not write your own
      Default server instructions.
- [ ] Link users to `https://notifs-setup.4irl.app/?app=<app_id>&user=<user_id>&name=<display name>` instead of
      writing your own ntfy install instructions.
- [ ] Capture the provision response (`token`) on first response — it is never re-shown.

## Related docs
- `ARCHITECTURE.md` — how the service works + Cloudflare/auth config, including the heartbeat watchdog
  operator runbook and the debugging quick reference.
- `README.md` — components, the local stack and delivery-api's runtime configuration.
- `admin-ui-same-origin` plan (`~/code/plans/4irl-notifs/`) — the admin console architecture.
