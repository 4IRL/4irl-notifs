# Integrating an app with 4irl-notifs

**Audience:** an engineer (or an LLM) wiring a new client application (e.g. `urls4irl`,
`tasktracker`) into the 4IRL notification service so it can send personalized notifications to its
users. This is a **self-service integration guide** — it is not about deploying the notification
service itself (that is `deploy-runbook.md`).

If you read nothing else, read the **Mental model** and **Integration in 5 steps** sections.

---

## Mental model

The notification service is two hosts:

| Host | What it is | How you authenticate to it |
|---|---|---|
| `https://notifs-api.4irl.app` | **Provisioning API** — creates users/tokens/ACLs. | Cloudflare Access **service token** (your app's own). |
| `https://notifs.4irl.app` | **ntfy server** — the actual pub/sub. You publish and subscribe here. | An **ntfy access token** (`Authorization: Bearer …`). |

You provision against the **provisioning API**, then publish/subscribe against **ntfy**.

**Identity is the user's email.** The service hashes it into a stable, opaque `person_hash`
(16 chars). The same email is the same person across every 4IRL app — you never send or store the
raw email in a topic. You do **not** compute the hash yourself; the API returns it.

**There are two kinds of token, doing two different jobs:**

| Token | Minted by | Scope | Lives where | Used to |
|---|---|---|---|---|
| **Publisher token** | `POST /v1/provision-app` (once per app) | write-only on `{app_id}-*` | your app's **backend** secret store | **publish** notifications |
| **Subscriber token** | `POST /v1/provision` (once per user) | read-only on `{app_id}-{person_hash}-*` | the user's **client** (or your backend, if you relay) | **receive** notifications |

**Topic naming** (you choose `{channel}`):

```
{app_id}-{person_hash}-{channel}     ← one user's messages for one channel
{app_id}-{person_hash}-*             ← everything for one user (their subscriber token can read this)
{app_id}-*                           ← everything for your app (your publisher token can write this)
```

Example: `urls4irl-v4sf4e5teivpe3zi-alerts`.

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
**not** revoke the old one — rotation is by-issuing.)

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

Publish to the user's channel with your **publisher token**:

```
POST https://notifs.4irl.app/urls4irl-v4sf4e5teivpe3zi-alerts
Authorization: Bearer tk_<publisher-token>
Title: New shared URL
Priority: default

Alice added a link to your UTub.
```

The topic is `{app_id}-{person_hash}-{channel}`; you pick `{channel}` per notification type
(`alerts`, `digest`, …). Your publisher token can write any `urls4irl-*` topic, so no per-user
publish credential is needed. (This is the standard ntfy publish API — see ntfy docs for `Title`,
`Priority`, `Tags`, `Click`, attachments, etc.)

### Step 4 — Receive notifications (ntfy, on the user's side)

The user's client subscribes with the **subscriber token** (read-only on their `*` topics):

```
GET https://notifs.4irl.app/urls4irl-v4sf4e5teivpe3zi-alerts/json
Authorization: Bearer tk_<subscriber-token>
```

ntfy supports `/json` (streaming JSON), `/sse`, and WebSocket subscriptions, plus the ntfy
mobile/desktop apps. Two delivery models — pick one:
- **Client-direct:** hand the subscriber token to the user's browser/app; it subscribes to ntfy
  directly. Simplest; the token lives on the client.
- **Backend-relay:** your backend holds the subscriber token, subscribes, and forwards to the user
  over your own channel (WebSocket/SSE/web-push). Keeps the token server-side.

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

## API reference — `notifs-api.4irl.app`

All calls require the two `CF-Access-Client-*` headers. All bodies + responses are JSON.

| Method & path | Body | Success response |
|---|---|---|
| `POST /v1/provision-app` | `{ "app_id" }` | `{ app_id, publisher_user_id, topic_pattern, token }` |
| `POST /v1/provision` | `{ "app_id", "email" }` | `{ user_id, app_id, person_hash, topic_pattern, token }` |
| `POST /v1/deprovision` | `{ "app_id", "email" }` **or** `{ "app_id", "user_id" }` | `{ user_id, app_id, removed }` |
| `GET /v1/users` | — | `{ users: [{ user_id, apps, topic_patterns }] }` |
| `DELETE /v1/users/{user_id}` | — | `{ user_id, deleted }` |
| `GET /healthz` | — | `ok` (behind Access) |

Notes:
- `user_id` is always the derived ntfy id `u_<person_hash>`. `email` must be a valid address
  (validated after trim + lowercase — case-insensitive; `Alice@x.com` == `alice@x.com`).
- Errors are `{ "error": "<message>" }` with the matching HTTP status (`400` invalid input,
  `404` user does not exist, `403`/redirect = missing/invalid service token, `500` internal).
- `GET /v1/users` and `DELETE /v1/users/{user_id}` are cross-app management surfaces — most app
  integrations only need `provision-app`, `provision`, and `deprovision`.

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
- [ ] Publish to `{app_id}-{person_hash}-{channel}` with the publisher token.
- [ ] User's client subscribes to `{app_id}-{person_hash}-{channel}` with the subscriber token.
- [ ] On opt-out / account deletion: call `POST /v1/deprovision`.
- [ ] All mint responses (`token`) are captured on first response — they are never re-shown.

## Related docs
- `deploy-runbook.md` — operating/deploying the notification service (operator-facing).
- `admin-ui-same-origin` plan (`~/code/plans/4irl-notifs/`) — the admin console architecture.
