# 4irl-notifs — Deploy & Infrastructure Runbook (human-in-the-loop)

Everything in this repository — code, CI, the deploy pipeline YAML, and the production compose
file — is committed and locally verified. What remains is the infrastructure a human must
provision by hand (GitHub and the Cloudflare dashboard). This runbook is the complete, ordered
checklist. Nothing here is automated by design: Cloudflare Access is **dashboard-managed (no
Terraform)** per the design doc, and repo/bot/secret creation requires account owner action.

Placeholders used throughout (the exact domain/subdomains are still an open design question —
substitute your choices consistently):

| Placeholder | Meaning | Example |
|---|---|---|
| `<zone>` | The Cloudflare-managed domain you own | `example.com` |
| `<admin-ui-hostname>` | Admin UI (Cloudflare Pages custom domain) | `notifs.example.com` |
| `<api-hostname>` | Provisioning API public hostname (tunnel + Access) | `notifs-api.example.com` |
| `<ntfy-hostname>` | ntfy server public hostname (tunnel; ntfy token auth) | `ntfy.example.com` |

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
beyond the built-in `GITHUB_TOKEN` (GHCR push). The deploy job requires all seven secrets below —
**net-new for this repo**, even though the VPS is shared with urls4irl (this stack gets its own
SSH keypair and its own Cloudflare Access Service Token; never reuse u4i's):

| Secret | Value |
|---|---|
| `NOTIFS_SSH_HOST` | The VPS SSH hostname fronted by the Cloudflare Access SSH application (same host u4i deploys to) |
| `NOTIFS_SSH_USERNAME` | The SSH user on the VPS for this stack |
| `NOTIFS_SSH_PRIVATE_KEY_FILENAME` | Filename to write the key as (e.g. `notifs_deploy_key`) |
| `NOTIFS_SSH_PRIVATE_KEY_VALUE` | The private key material (generate a NEW keypair; add the public key to the VPS user's `authorized_keys`) |
| `NOTIFS_SERVICE_TOKEN_ID` | Cloudflare Access Service Token **Client ID** for the SSH Access application (mint a new token; see step 7) |
| `NOTIFS_SERVICE_TOKEN_SECRET` | The matching Service Token **Client Secret** |
| `NOTIFS_NTFY_BASE_URL` | `https://<ntfy-hostname>` — written to `~/4irl-notifs/.env` on the VPS and consumed by the ntfy container as `NTFY_BASE_URL` |

## 4. DNS / hostnames

Decide the three hostnames (`<admin-ui-hostname>`, `<api-hostname>`, `<ntfy-hostname>`) under a
Cloudflare-managed zone. DNS records for `<api-hostname>` and `<ntfy-hostname>` are created
automatically when you add them as tunnel public hostnames (step 5); `<admin-ui-hostname>` is
created when you attach it as the Pages custom domain (step 6).

## 5. Persistent live-ingress Cloudflare Tunnel (net-new)

urls4irl's tunnel usage is **deploy-only** (SSH ProxyCommand). The provisioning API and ntfy need
a **persistent** tunnel on the VPS serving live HTTP — this is new infrastructure:

1. In Zero Trust → Networks → Tunnels, create a tunnel (e.g. `4irl-notifs`) and install
   `cloudflared` as a service on the VPS with the tunnel token (or reuse an existing persistent
   tunnel on that host if one already runs — just add the public hostnames).
2. Public hostname routes:
   - `<api-hostname>` → `http://127.0.0.1:8091` (provisioning-api)
   - `<ntfy-hostname>` → `http://127.0.0.1:8090` (ntfy)
3. The production compose file binds both ports to `127.0.0.1` only — the tunnel is the sole
   public entry point. `NTFY_BEHIND_PROXY=true` is already set in `docker-compose.prod.yml`.
4. **Do NOT put an Access application in front of `<ntfy-hostname>`.** ntfy's own auth is the
   boundary there (`auth-default-access: deny-all` + per-user tokens); the ntfy mobile/CLI
   clients cannot complete an OAuth redirect. The Access-gated surfaces are the API and the
   admin UI only.

## 6. Cloudflare Pages (admin UI)

1. Create a Pages project connected to the GitHub repo (native GitHub integration):
   - Root directory: `web`
   - Build command: `npm run build`
   - Build output directory: `dist`
   - Environment variable: `VITE_API_BASE_URL=https://<api-hostname>` (baked in at build time;
     without it the SPA calls same-origin paths that do not exist on Pages).
2. **Ordering gotcha:** attach the custom domain `<admin-ui-hostname>` to the Pages project
   **BEFORE** creating the Access application against that hostname (step 8). Access targets
   hostnames; creating the app first breaks the domain attachment flow.
3. Note: the production (custom-domain) deployment is protected by the Access application from
   step 8 — the Pages "Access policy" toggle only covers `*.pages.dev` preview URLs.

## 7. Zero Trust identity + Service Tokens

1. **OAuth IdP:** in Zero Trust → Settings → Authentication, add Google and/or GitHub as login
   methods (design decision: Google/GitHub OAuth; no one-time PIN).
2. **Service Tokens:** mint one Access Service Token **per consuming app** (urls4irl first) in
   Zero Trust → Access → Service Auth. Each consuming app calls the provisioning API with
   `CF-Access-Client-Id` / `CF-Access-Client-Secret` headers. Also mint the separate deploy
   token for SSH (step 3's `NOTIFS_SERVICE_TOKEN_ID`/`SECRET`) if you are not reusing an
   existing SSH Access application setup.

## 8. Cloudflare Access applications (dashboard-managed — NO Terraform)

Create two self-hosted Access applications:

1. **Provisioning API** — application domain `<api-hostname>`, with two Allow policies:
   - Policy 1 (humans): Allow → include your Google/GitHub-authenticated admin identities
     (e.g. specific emails). Admin-UI users reach the API through their existing Access SSO
     session — no extra login prompt.
   - Policy 2 (machines): a policy with the **Service Auth** action whose include rule is the
     Service Token(s) from step 7 (one per consuming app). The action must be Service Auth —
     an identity-based Allow action would redirect token requests to login.
   - **CORS settings on this application** (the admin UI calls the API cross-origin): allow
     origin `https://<admin-ui-hostname>`, allow the methods `GET POST DELETE OPTIONS` and the
     `Content-Type` header, and enable **Allow credentials**. Access must also be allowed to
     answer preflight (OPTIONS) requests — enable the bypass-preflight option on the app. The
     web client already sends `credentials: 'include'` so the Access cookie crosses hostnames.
2. **Admin UI** — application domain `<admin-ui-hostname>`, one Allow policy for the same
   Google/GitHub identities. Create this AFTER the custom domain is attached to Pages (step 6).

## 9. Post-provisioning verification

1. PR CI: `gh pr checks <PR#> --repo <slug>` (expected `4IRL/4irl-notifs` — use whatever slug
   step 1 created) with `--watch` → every check green.
2. Merge the PR → `Build and Deploy Production Stack` runs: build pushes
   `ghcr.io/4irl/4irl-notifs-provisioning-api:latest`; deploy SCPs the compose file + ntfy
   config into `~/4irl-notifs/` on the VPS and brings the stack up; the workflow's verify step
   confirms both services are running.
3. From a trusted machine:
   - `curl https://<ntfy-hostname>/v1/health` → `{"healthy":true}` (no Access in the way).
   - `curl https://<api-hostname>/healthz` unauthenticated → an Access redirect/403 (the gate
     works); with a Service Token (`CF-Access-Client-Id`/`-Secret` headers) → `ok`.
4. Open `https://<admin-ui-hostname>`, authenticate via OAuth, provision a test user into an
   app, confirm the token reveal, then publish/subscribe against
   `https://<ntfy-hostname>/<app>-test` with that token.
5. Deprovision the test user from the admin UI and confirm the token no longer works (401).

## 10. Open items intentionally left to the maintainer

- Exact domain/subdomain choices (design-doc open question) — substitute consistently above.
- Whether to revisit Terraform-managed Access policy if the policy surface grows (explicitly
  deferred by the design doc).
- The GitHub issue for this initiative (create one and link the PR with `Closes #<n>` if you
  want the issue-linking convention satisfied retroactively).
