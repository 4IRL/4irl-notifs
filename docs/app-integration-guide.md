# Integrating an app with 4irl-notifs

**Audience:** an engineer (or an LLM) wiring a new client application (e.g. `urls4irl`,
`tasktracker`) into the 4IRL notification service so it can send personalized notifications to its
users. This is a **self-service integration guide** — it is not about deploying the notification
service itself (that is `ARCHITECTURE.md`).

If you read nothing else, read the **Mental model** and **Integration in 5 steps** sections.

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
  "token": "tk_<subscriber-token>"
}
```

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

> **iOS clients need one extra step:** the ntfy iOS app's **Default Server** must be set to exactly
> `https://notifs.4irl.app`, or notifications silently never arrive. See **Usage recommendations** below.

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

### Operator runbook: ntfy publisher for an app

Recipient deliveries publish with a per-app ntfy publisher token that delivery-api stores sealed. It is
minted once through provisioning-api by an explicit operator call, never lazily on submit:

- Mint on the VPS: `curl -X POST http://127.0.0.1:${DELIVERY_PORT}/v1/admin/apps/<app>/ntfy-publisher`
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
  `curl -X DELETE http://127.0.0.1:${DELIVERY_PORT}/v1/admin/apps/<app>/ntfy-publisher` (204, idempotent;
  it removes only delivery-api's copy and revokes nothing in ntfy). Re-mint the publisher before
  recipient sends are expected to work again.

---

## iOS subscriber login — set a password

The subscriber token (Step 2/Step 4) is what a **programmatic or Android** client uses to subscribe.
**iOS is different:** the ntfy iOS app's subscribe screen has **no token field** — an iOS user signs into
the ntfy app with a **username + password**, not a token. The username is the derived ntfy user id
`u_<person_hash>` (the `user_id` from `/v1/provision`); the password is one **your app sets for them** via
`POST /v1/set-password`. So for any user who will subscribe on iOS, provision them first, then let them
choose a password and call this endpoint.

```
POST https://notifs-api.4irl.app/v1/set-password
Content-Type: application/json
CF-Access-Client-Id: <id>
CF-Access-Client-Secret: <secret>

{ "app_id": "urls4irl", "email": "alice@example.com", "password": "her-chosen-password" }
```

Response:

```json
{
  "user_id": "u_v4sf4e5teivpe3zi",
  "app_id": "urls4irl",
  "updated": true
}
```

The user then logs into the ntfy iOS app with **username `u_v4sf4e5teivpe3zi` + that password** (after
setting the Default Server to `https://notifs.4irl.app`, see below), and their existing read ACL lets
them subscribe to their own `{app_id}-{person_hash}-*` topics and `{app_id}-broadcast`.

**Rules:**
- **Provision first.** The user must already exist (from `POST /v1/provision`); setting a password on an
  un-provisioned email returns `404 { "error": "user does not exist" }`. You cannot set a password on a
  user that hasn't been created.
- **Password policy: 8–128 characters.** Anything shorter/longer/empty →
  `400 { "error": "invalid password" }`. The password is used verbatim (no trimming). Collect it from the
  user; do not reuse the subscriber token as a password.
- **Re-callable.** `change-pass` overwrites the existing password, so calling `set-password` again just
  resets it — use this for a "change my ntfy password" flow.
- **Android/programmatic clients don't need this** — they authenticate with the subscriber token. Only the
  iOS app (and any other username+password ntfy client) needs a password set.

---

## Usage recommendations (this deployment)

The 5 steps above are the mechanics; this is how to use them well against `notifs.4irl.app`.

**Delivery: prefer client-direct.** This deployment is built for client-direct delivery — the user's
device subscribes to ntfy itself. Reach for backend-relay only when a token must never live on the
client; it makes your backend own the reconnection/offline story ntfy otherwise handles for free.

**iOS (ntfy app) — the Default Server gotcha.** iOS can't hold a background connection, so iOS delivery
goes through APNs bridged via ntfy.sh. Two consequences for you:
- The user's ntfy iOS app **Default Server** (or per-subscription server) must equal
  `https://notifs.4irl.app` **exactly** — https, no trailing slash. Any mismatch and the wake-up can't
  be matched, so notifications **silently never arrive**. Put this in your iOS onboarding steps.
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
| `POST /v1/provision`         | `{ "app_id", "email" }`                                  | `{ user_id, app_id, person_hash, topic_pattern, broadcast_topic, token }` |
| `POST /v1/deprovision`       | `{ "app_id", "email" }` **or** `{ "app_id", "user_id" }` | `{ user_id, app_id, removed }`                                            |
| `POST /v1/set-password`      | `{ "app_id", "email", "password" }`                      | `{ user_id, app_id, updated }`                                            |
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
- **`POST /v1/set-password`** sets a person's ntfy password so they can log into the ntfy **iOS** app
  directly (username + password — see "iOS subscriber login" below). The user **must already be
  provisioned**: setting a password on a not-yet-provisioned email returns `404 { "error": "user does not
  exist" }`. The password is validated at **8–128 characters** (`400 { "error": "invalid password" }`
  otherwise; `app_id` and `email` are validated first, in that order). `change-pass` overwrites any
  existing password, so calling it again simply resets the credential. The response `{ user_id, app_id,
  updated }` echoes the derived `user_id` (`u_<person_hash>`) with `updated: true`.
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
- [ ] iOS users: call `POST /v1/set-password` (after provisioning) so they have a username+password to log
      into the ntfy iOS app with (`u_<person_hash>` + the password) — the iOS app has no token field.
- [ ] iOS users: ntfy app **Default Server** set to exactly `https://notifs.4irl.app` (else silent no-delivery).
- [ ] Bulk/periodic sender: publisher rate-limit raised/exempted by operator; many-per-user sends coalesced into a digest.
- [ ] All mint responses (`token`) are captured on first response — they are never re-shown.

## Related docs
- `ARCHITECTURE.md` — how the service works + Cloudflare/auth config.
- `admin-ui-same-origin` plan (`~/code/plans/4irl-notifs/`) — the admin console architecture.
