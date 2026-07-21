# 4irl-notifs — Deploy & Infrastructure Runbook (human-in-the-loop)

Everything in this repository — code, CI, the deploy pipeline YAML, and the production compose
file — is committed and locally verified. What remains is the infrastructure a human must
provision by hand (GitHub and the Cloudflare dashboard). This runbook is the complete, ordered
checklist. Nothing here is automated by design: Cloudflare Access is **dashboard-managed (no
Terraform)** per the design doc, and repo/bot/secret creation requires account owner action.

The three hostnames (decided) live under the `4irl.app` Cloudflare zone:

| Role | Hostname |
|---|---|
| Admin UI (Cloudflare Pages custom domain) | `notifs-admin.4irl.app` |
| Provisioning API (tunnel + Access) | `notifs-api.4irl.app` |
| ntfy server (tunnel; ntfy token auth) | `notifs.4irl.app` |

---

## 1. GitHub repository + remote

1. Create the GitHub repository (expected slug `4IRL/4irl-notifs` — the slug in `CLAUDE.md` is
   still marked unconfirmed; whatever you create, update the `Repo slug` seam key in `CLAUDE.md`).
2. Add the remote and push:
   - `git remote add origin git@github.com:4IRL/4irl-notifs.git`
   - Push `main`, then push `feature/notification-service`.
3. Open a single PR `feature/notification-service` → `main`. CI (`CI.yml`) runs on the PR:
   format → lint → test (Go unit + Vitest) / End-to-end (Playwright) / Integration
   (docker-compose stack on the runner) / Build image (buildx + smoke test). All of these were
   verified green locally before handoff.

## 2. GitHub-App bot (push/PR identity)

Set up a GitHub App bot so pushes and PRs come from a bot identity rather than personal
credentials (see the `/github-app-bot` skill in the workflow tooling, and urls4irl's
`u4i-claude-code[bot]` setup as the reference). Then fill in the `Bot identity`,
`Bot push script`, and `Token generator` seam keys in `CLAUDE.md` (currently `n/a`).

## 3. GitHub Actions secrets (deploy pipeline)

`prod-build-and-deploy.yml` runs when a PR merges to `main`. The build job needs no secrets
beyond the built-in `GITHUB_TOKEN` (GHCR push). The VPS-deploy job requires the six secrets below.
These are **repo-scoped secrets on `4IRL/4irl-notifs`** — although `PROD_SSH_*` shares u4i's naming,
these are this repo's own independent values (its own SSH keypair, its own Cloudflare Access Service
Token; never reuse u4i's). The ntfy base URL is **no longer a secret** — it's a fixed public
hostname baked into `docker-compose.prod.yml`'s default. The `deploy-admin-ui` (Pages) job needs
**two more** secrets — `CLOUDFLARE_API_TOKEN` and `CLOUDFLARE_ACCOUNT_ID` — documented in §6.

| Secret | Value |
|---|---|
| `PROD_SSH_HOST` | The VPS SSH hostname fronted by the Cloudflare Access SSH application (same host u4i deploys to) |
| `PROD_SSH_USERNAME` | The SSH user on the VPS for this stack |
| `PROD_SSH_PRIVATE_KEY_FILENAME` | Filename to write the key as (e.g. `notifs_deploy_key`) |
| `PROD_SSH_PRIVATE_KEY_VALUE` | The private key material (generate a NEW keypair; add the public key to the VPS user's `authorized_keys`) |
| `CF_ACCESS_CLIENT_ID` | Cloudflare Access Service Token **Client ID** for the SSH Access application (mint a new token; see step 7) |
| `CF_ACCESS_CLIENT_SECRET` | The matching Service Token **Client Secret** |

## 4. DNS / hostnames

The three hostnames all sit under the `4irl.app` Cloudflare zone. DNS records for
`notifs-api.4irl.app` and `notifs.4irl.app` are created automatically when you add them as tunnel
public hostnames (step 5); `notifs-admin.4irl.app` is created when you attach it as the Pages
custom domain (step 6).

## 5. Live-ingress hostnames on the existing VPS tunnel (reuse)

The VPS already runs a **permanent, remotely-managed (token) Cloudflare Tunnel** that is already
**multi-route**: it fronts urls4irl's live web hostname *and* an SSH hostname (behind a Cloudflare
Access self-hosted SSH app) that the deploy pipeline connects to ephemerally via
`cloudflared access ssh` (ProxyCommand). "Deploy-only" referred to that SSH *client session's*
lifecycle — the tunnel itself is permanent. Notifs runs on the **same host**, so we **reuse that
tunnel** and add public hostname routes — no new tunnel, no second `cloudflared` daemon, no new
tunnel credential. Because the tunnel is dashboard-managed, adding hostnames is a **dashboard-only**
change: no `config.yml` edit and no `cloudflared` restart, so urls4irl's live traffic is never
touched. (Notifs' own deploy reuses this same SSH hostname/Access app but with its **own** SSH
keypair and Service Token, authorized by its **own distinct Service Auth policy** on that app —
see §3 and §8 item 3.)

1. In Zero Trust → Networks → Tunnels, open the **existing** tunnel and add the public hostname
   routes below. (Do NOT create a `4irl-notifs` tunnel — that path is retired; a dedicated tunnel
   was considered and rejected since both services share one origin host. See the design note at
   the end of this section.)
2. Public hostname routes (service type **HTTP**, not HTTPS — Cloudflare terminates TLS at the edge):
   - `notifs-api.4irl.app` → `http://127.0.0.1:8091` (provisioning-api)
   - `notifs.4irl.app` → `http://127.0.0.1:8090` (ntfy)

   These point **directly at the container ports** — NOT through urls4irl's nginx. nginx belongs
   only to u4i's `urls.4irl.app` route (tunnel → nginx → localhost); tunnel routes are per-hostname
   and independent, so notifs needs no reverse-proxy hop. Routing ntfy direct also avoids nginx's
   SSE/streaming-buffering pitfalls on ntfy's subscription endpoints. (That u4i's `urls.4irl.app`
   route reaches nginx on host `localhost` also confirms the connector resolves `localhost` to the
   VPS host, so these `127.0.0.1` bindings are reachable the same way.)
3. The production compose file binds both ports to `127.0.0.1` only — the tunnel is the sole
   public entry point. `NTFY_BEHIND_PROXY=true` is already set in `docker-compose.prod.yml`.
4. **Do NOT put an Access application in front of `notifs.4irl.app`.** ntfy's own auth is the
   boundary there (`auth-default-access: deny-all` + per-user tokens); the ntfy mobile/CLI
   clients cannot complete an OAuth redirect. The Access-gated surfaces are the API and the
   admin UI only.

**Design note — reuse vs. dedicated tunnel.** A dedicated notifs tunnel was considered and
rejected. Both services share one origin host, so a single tunnel routing many hostnames via
public-hostname/ingress rules is the intended Cloudflare pattern — "one tunnel per host," not "per
hostname." The usual reuse concern (a `config.yml` edit + connector reload disrupting the co-tenant)
does not apply here because the tunnel is **remotely managed**: notifs hostnames are added in the
dashboard with no restart. A separate tunnel would add a second daemon, credential, and monitoring
surface on the same box for no isolation gain, since one person owns both stacks. Access policies
(steps 7–8) sit in front of each hostname independently regardless of which tunnel serves it.

## 6. Cloudflare Pages (admin UI)

**Deploy method: Wrangler in GitHub Actions** (decided over native Git integration — keeps the
deploy in the existing CI pipeline so the Vitest/Playwright suite gates it, and avoids installing
the Cloudflare Pages GitHub App on the org). The build/deploy is codified in
`.github/workflows/pages-deploy.yml`, wired as the `deploy-admin-ui` job in
`prod-build-and-deploy.yml` (runs on merge to `main`, parallel to the API build/deploy). It runs
`npm ci` + `npm run build` (with `VITE_API_BASE_URL` baked in) then
`wrangler pages deploy dist --project-name=notifs-admin --branch=main`.

Human provisioning (one-time):

1. **Cloudflare API token** — create at Cloudflare dashboard → My Profile → API Tokens → Create
   Token, scoped **only** to `Account · Cloudflare Pages · Edit` (nothing more). Store as the GitHub
   repo secret `CLOUDFLARE_API_TOKEN`.
2. **Account ID** — copy from any zone's Overview (right sidebar) or the dashboard URL. Store as the
   GitHub repo secret `CLOUDFLARE_ACCOUNT_ID`. (Not sensitive, but the workflow reads it as a secret.)
3. **Create the Pages project** (must exist before the first deploy; a Direct-Upload project, NOT
   Git-connected): `wrangler pages project create notifs-admin --production-branch=main`, or in the
   dashboard → Workers & Pages → Create → Pages → **Direct Upload**, name `notifs-admin`.
4. **Ordering gotcha:** attach the custom domain `notifs-admin.4irl.app` to the Pages project
   **BEFORE** creating the Access application against that hostname (step 8). Access targets
   hostnames; creating the app first breaks the domain attachment flow. Cloudflare auto-creates the
   proxied CNAME (the `4irl.app` zone is already in the account).
5. **Lock down preview URLs.** The custom domain is protected by the step-8 Access app, but the
   Pages "Access policy" toggle covers ONLY `*.pages.dev` preview deploys — and every branch gets a
   public preview by default. For an admin surface, either disable preview deployments or enable the
   Pages Access-policy toggle so `*.pages.dev` is gated too. Otherwise the admin UI is reachable
   unauthenticated at a preview URL.

## 7. Zero Trust identity + Service Tokens

1. **OAuth IdP (GitHub):** decided — **GitHub only** (no Google, no one-time PIN). The Zero Trust
   org/team domain already exists (urls4irl uses Access), so this *adds a login method* to it.
   First check Settings → Authentication → Login methods — if GitHub is already present (from
   u4i), reuse it. Otherwise: register a GitHub OAuth App (under the 4IRL org's Developer settings)
   with callback URL `https://<team>.cloudflareaccess.com/cdn-cgi/access/callback`, then add it in
   Zero Trust → Settings → Authentication → Login methods → GitHub with the App's Client ID +
   Secret, and Test. Authorization (which people) is enforced in the step-8 Access-app policies —
   the IdP only authenticates. To allow by "member of the 4IRL GitHub org," enable org info on the
   GitHub IdP connection.
2. **Service Tokens:** mint one Access Service Token **per consuming app** (urls4irl first) in
   Zero Trust → Access → Service Auth. Each consuming app calls the provisioning API with
   `CF-Access-Client-Id` / `CF-Access-Client-Secret` headers.
3. **Deploy Service Token (SSH):** mint a separate token for the GitHub deploy pipeline (step 3's
   `CF_ACCESS_CLIENT_ID`/`CF_ACCESS_CLIENT_SECRET`) — its own token, never u4i's. Minting alone does not grant
   access; it must be authorized by a **distinct Service Auth policy** on the SSH Access app
   (step 8, item 3), exactly as urls4irl's deploy is gated by its own such policy.

## 8. Cloudflare Access applications (dashboard-managed — NO Terraform)

Create two **new** self-hosted Access applications (API + Admin UI), then add one policy to the
**existing** SSH deploy app:

1. **Provisioning API** — application domain `notifs-api.4irl.app`:
   - Policy 1 (humans): Allow → include **GitHub Organization `4IRL`** (decided — org membership,
     not an email list). Admin-UI users reach the API through their existing Access SSO session —
     no extra login prompt. **Prereq:** the GitHub IdP must expose org membership and the 4IRL org
     must have approved the Cloudflare Access OAuth app (a restricted third-party-app policy
     yields an empty org list → silent deny). See §7.
   - Policy 2 (machines): a policy with the **Service Auth** action whose include rule is the
     Service Token(s) from step 7 (one per consuming app). The action must be Service Auth —
     an identity-based Allow action would redirect token requests to login. **Deferred** until the
     first consuming app onboards; the human policy alone covers the admin UI today.
   - **CORS settings on this application** (the admin UI calls the API cross-origin):
     - **Allow-Origin:** `https://notifs-admin.4irl.app` (a specific origin — do NOT use "allow all
       origins"; the CORS spec forbids `*` together with credentials).
     - **Allow-Methods:** `GET, POST, DELETE` (do NOT add `OPTIONS` — it's the preflight mechanism,
       not an app method; Access handles preflight itself).
     - **Allow-Headers:** `Content-Type`.
     - **Allow-Credentials:** ON (the SPA sends `credentials: 'include'` so the Access cookie
       crosses hostnames).
     - **"Bypass OPTIONS requests to origin":** leave **OFF**. With CORS configured, Access answers
       the preflight itself. Turning it ON forwards OPTIONS to the origin AND **removes all CORS
       settings** on the app — the opposite of what's wanted.
     - Max-Age: optional (e.g. `600` to cache preflight); empty is fine.
2. **Admin UI** — application domain `notifs-admin.4irl.app`, one Allow policy for the same
   **GitHub Organization `4IRL`** members. No CORS (it is the origin, not called cross-origin).
   Create this AFTER the custom domain is attached to Pages (step 6).

Then, on the **existing SSH Access application** (reused — the self-hosted SSH app u4i already
deploys through; do NOT create a new one):

3. **SSH deploy (existing app, new policy)** — add a **distinct Service Auth policy** whose
   include rule is *only* notifs' deploy Service Token from step 7 (item 3). Action MUST be
   **Service Auth** (an identity Allow would redirect the token to login). This mirrors
   urls4irl's own distinct Service Auth policy on the same app; the two policies coexist and are
   revocable independently. Without this policy, `cloudflared access ssh --id … --secret …` in
   the deploy pipeline is rejected and the SCP/deploy step fails.

## 9. Post-provisioning verification

1. PR CI: `gh pr checks <PR#> --repo <slug>` (expected `4IRL/4irl-notifs` — use whatever slug
   step 1 created) with `--watch` → every check green.
2. Merge the PR → `Build and Deploy Production Stack` runs: build pushes
   `ghcr.io/4irl/4irl-notifs-provisioning-api:latest`; deploy SCPs the compose file + ntfy
   config into `/home/4irl-notifs/` on the VPS and brings the stack up; the workflow's verify step
   confirms both services are running.
3. From a trusted machine:
   - `curl https://notifs.4irl.app/v1/health` → `{"healthy":true}` (no Access in the way).
   - `curl https://notifs-api.4irl.app/healthz` unauthenticated → an Access redirect/403 (the gate
     works); with a Service Token (`CF-Access-Client-Id`/`-Secret` headers) → `ok`.
4. Open `https://notifs-admin.4irl.app`, authenticate via OAuth, provision a test user into an
   app, confirm the token reveal, then publish/subscribe against
   `https://notifs.4irl.app/<app>-test` with that token.
5. Deprovision the test user from the admin UI and confirm the token no longer works (401).

## 10. Wave 2 provisioning — personalized notifications (post-merge, human)

Wave 2 (the personalized-notifications initiative) shipped the person-service Cloudflare Worker +
D1 reverse-index (personHash → email), the publisher/subscriber ACL split (publisher identity
`{app_id}-publisher` holds write-only `wo` on `{app_id}-*`; each end-user holds read-only on
`{app_id}-{personHash}-*`), the Go dual-write, and the admin UI's People view. All **code** is
merged; the 8 infrastructure steps below are strictly operator work — none were executed by
automation, and **no token, Service-Token client-id/secret, or account ID is committed anywhere**
(placeholders only, same as §§3/6/8 above).

1. **Create the D1 database + apply the migration.** From `person-service/`:
   `wrangler d1 create person-service` → copy the returned `database_id` into
   `person-service/wrangler.toml`, replacing the committed placeholder
   `00000000-0000-0000-0000-000000000000` in the `[[d1_databases]]` block (commit that as a
   follow-up PR — it's an identifier the repo does track once minted, not a secret; per this
   repo's ask-before-committing-IDs convention, the maintainer commits it themselves rather than
   an agent doing so). Then apply the schema: `wrangler d1 migrations apply person-service
   --remote`. The migration (`migrations/0001_create_person.sql`) creates
   `person(person_hash PK, email, created_at)` plus `idx_person_email`.
2. **Deploy the Worker.** Preferred: merge-triggered CI —
   `.github/workflows/worker-deploy.yml` runs as the `deploy-person-service` job in
   `prod-build-and-deploy.yml`, using repo secrets `CLOUDFLARE_API_TOKEN` +
   `CLOUDFLARE_ACCOUNT_ID` (the same two secrets §6 already wired up for Pages). **GOTCHA:** the
   existing `CLOUDFLARE_API_TOKEN` was scoped only to `Account · Cloudflare Pages · Edit` (per
   §6 item 1); Worker deploys additionally need `Account · Workers Scripts · Edit` and
   `Account · D1 · Edit` — extend the token's scopes (or mint a fresh one and overwrite the
   secret). Until the real `database_id` lands in `wrangler.toml` (step 1), the deploy job fails
   by design — the workflow's own header comment says so; that's expected scaffolding, not a bug.
   Manual alternative: `cd person-service && npm ci && npx wrangler deploy`.
3. **Worker hostname + DNS + route.** Decide the Worker custom domain — suggested
   `notifs-people.4irl.app` (`4irl.app` zone, same as the other three hostnames). Add it as a
   Custom Domain on the person-service Worker (dashboard → Workers & Pages → person-service →
   Settings → Domains & Routes; Cloudflare auto-creates the proxied DNS record, same pattern as
   §4/§6), or fill in the `[[routes]]` block already commented out in `wrangler.toml`.
   **IMPORTANT lockstep:** `pages-deploy.yml` bakes
   `VITE_PERSON_SERVICE_URL=https://notifs-people.4irl.app` into the admin-UI build at deploy
   time — if a different hostname is chosen, update that env value in `pages-deploy.yml` in the
   same follow-up PR as the `database_id` (step 1). Until the Worker and its Access app (step 4)
   are live, the admin UI's People view renders a graceful load-error line; user management is
   unaffected.
4. **Cloudflare Access app on the Worker hostname.** New self-hosted Access application, domain =
   the Worker hostname from step 3. Policies (mirror §8 item 1's structure):
   - Policy 1 (humans, for the admin-UI People view): Allow → **GitHub Organization `4IRL`**
     (same IdP/org-visibility prereqs as §8 item 1 / §7 item 1).
   - Policy 2 (machines, for the Go dual-write): action **Service Auth** (NOT an identity Allow —
     an Allow action would redirect token requests to login) including the Service Token from
     step 5.
   - **CORS settings on this app** (the admin UI reads `GET /people` cross-origin with
     credentials): **Allow-Origin** `https://notifs-admin.4irl.app` (never "all origins" — the
     CORS spec forbids `*` together with credentials); **Allow-Methods** `GET`; **Allow-Headers**
     `Content-Type`; **Allow-Credentials** ON; leave **"Bypass OPTIONS requests to origin"** OFF
     (same rationale as §8 item 1: Access answers preflight itself, and the bypass toggle wipes
     the app's CORS config).
   - Reminder: the Worker performs **no auth of its own** — this Access app is the entire
     boundary in front of it, so do not skip it.
5. **Mint the person-service Service Token.** Zero Trust → Access → Service Auth → create a token
   dedicated to the provisioning-api's dual-write (name it e.g.
   `notifs-provisioning-api → person-service`). Record `<CLIENT_ID>` / `<CLIENT_SECRET>` for step
   6; include this same token in step 4's Service Auth policy.
6. **Register the dual-write credentials as GitHub Actions secrets (no VPS `.env`).** The
   dual-write credentials are delivered as **Docker Compose secrets**, the same pattern urls4irl
   uses — *not* a hand-placed `.env`. Set two repo secrets:
   `PERSON_SERVICE_ACCESS_CLIENT_ID` / `PERSON_SERVICE_ACCESS_CLIENT_SECRET` (the step-5 values).
   That is the only manual action — everything else is automated:
   - `prod-deploy.yml` writes them to `./secrets/*` on the VPS from those GitHub secrets just
     before `docker compose up`, then `rm -rf ./secrets/` immediately after — so no plaintext
     secret persists on the host.
   - `docker-compose.prod.yml` mounts them as `/run/secrets/<NAME>` (tmpfs, inside the container
     only — the values never enter the container environment / `docker inspect`).
   - provisioning-api reads them via the `<KEY>_FILE` convention (`internal/secretenv`).
   - `PERSON_SERVICE_URL` is **not** a secret; it is baked into the compose default
     (`https://notifs-people.4irl.app`, like `NTFY_BASE_URL`).
   Empty/unset secrets are safe: they write empty files, leaving the dual-write auth unset (it
   fails closed and is swallowed — core provisioning is unaffected). A merge to `main`
   (re)deploys and applies them; verify from the container logs the startup line
   `person-service dual-write` shows `enabled=true` and `auth_configured=true`.
7. **Per-app publisher identities.** For each consuming app (urls4irl first):
   ```
   curl -X POST https://notifs-api.4irl.app/v1/provision-app \
     -H 'Content-Type: application/json' \
     -H 'CF-Access-Client-Id: <consuming-app-client-id>' \
     -H 'CF-Access-Client-Secret: <consuming-app-client-secret>' \
     -d '{"app_id":"urls4irl"}'
   ```
   → the response carries `publisher_user_id` (`{app_id}-publisher`), `topic_pattern`
   (`{app_id}-*`), and the write-only `token` — **shown once**; hand it to that app's backend
   secret store immediately. Repeat calls mint an *additional* token rather than replacing the
   old one (rotation-by-issuing); revoke a stale one explicitly if needed:
   `docker compose -f docker-compose.prod.yml exec provisioning-api ntfy token remove
   {app_id}-publisher <token>` (the provisioning-api container bundles the `ntfy` CLI against
   the same `auth.db` the server uses — see `docker-compose.prod.yml`). The consuming app's
   backend publishes personalized messages to `{app_id}-{personHash}-{channel}` topics with this
   token; it learns each user's personHash from the `/v1/provision` response's `person_hash`
   field.
8. **Per-consuming-app Access Service Tokens on the provisioning API.** This activates §8 item
   1's deferred "Policy 2 (machines)": mint one Service Token per consuming app (Zero Trust →
   Access → Service Auth), add them to a Service Auth policy on the `notifs-api.4irl.app` Access
   app. Each app calls `/v1/provision`, `/v1/deprovision` (now also accepts `email` in the body
   as an alternative to `user_id`, resolving to the same derived `u_<personHash>` ntfy user id —
   the deprovision response itself carries only `user_id`/`app_id`/`removed`, not a separate
   `person_hash` field), and `/v1/provision-app`, each with its own
   `CF-Access-Client-Id`/`-Secret` headers.

**Wave 2 verification:**

- (a) After step 2/3, `curl https://<worker-hostname>/people` unauthenticated → an Access
  redirect/403 (the gate works).
- (b) With the step-5 Service Token headers (`CF-Access-Client-Id`/`-Secret`) → `200`
  `{"people":[...]}` (empty list initially).
- (c) Provision a test user with an email from the admin UI → the People table shows the
  personHash → email row (dual-write worked).
- (d) Publish to `{app_id}-{personHash}-test` with the app's publisher token from step 7 → `200`,
  and the user's subscriber token can read it; the publisher token gets `403` on read (write-only)
  and `403` publishing outside `{app_id}-*`.

## Operations: deploys, ntfy config, and ntfy version bumps

The deploy runs `docker compose up -d --remove-orphans` (no `--force-recreate`) so compose
recreates **only** services whose image digest or compose definition changed. Consequences:

- **Routine API deploy** (new `provisioning-api:latest`): only provisioning-api is recreated; the
  **ntfy container keeps running**, so its long-lived subscriber connections survive the deploy.
- **ntfy config change** (`ntfy/server.yml` contents): compose does **not** watch bind-mounted file
  contents, so a config-only change won't restart ntfy on deploy. After the deploy SCPs the new
  file, run a targeted recreate on the VPS: `docker compose -f docker-compose.prod.yml up -d
  --force-recreate ntfy` (drops subscribers once, intentionally).
- **ntfy version bump:** update the tag in **all three** lockstep locations — `Dockerfile` (the
  bundled CLI stage), `docker-compose.prod.yml`, and `docker-compose.yml` — in one commit. Keeping
  the bundled CLI and the server in sync avoids an `auth.db` format skew. The compose `image:` tag
  change is a definition change, so the next deploy recreates ntfy automatically (subscribers drop
  once — expected for a server upgrade). Bump `provisioning-api`'s own rebuild too, since the
  Dockerfile changed.

## 11. Open items intentionally left to the maintainer

- Whether to revisit Terraform-managed Access policy if the policy surface grows (explicitly
  deferred by the design doc).
- The GitHub issue for this initiative (create one and link the PR with `Closes #<n>` if you
  want the issue-linking convention satisfied retroactively).
