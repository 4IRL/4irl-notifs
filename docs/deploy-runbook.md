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

## 10. Open items intentionally left to the maintainer

- Whether to revisit Terraform-managed Access policy if the policy surface grows (explicitly
  deferred by the design doc).
- The GitHub issue for this initiative (create one and link the PR with `Closes #<n>` if you
  want the issue-linking convention satisfied retroactively).
