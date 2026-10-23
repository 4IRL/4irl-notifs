# LOCAL/CI ONLY: runs the real person-service Worker under `wrangler dev --local` (miniflare + local D1)
# so integration tests exercise the same code production runs. Never deployed; production runs the
# Worker on Cloudflare (see worker-deploy.yml). Build context is person-service/.
# Node major matches worker-deploy.yml ('24'); wrangler comes from package-lock.json via `npm ci`.
FROM node:24.21.0-bookworm-slim

WORKDIR /app

COPY package.json package-lock.json ./
RUN npm ci

COPY wrangler.toml tsconfig.json ./
COPY migrations ./migrations
COPY src ./src

EXPOSE 8787

# Apply migrations to the local D1 (state in /app/.wrangler, lost with the container), then serve.
CMD ["sh", "-c", "npx wrangler d1 migrations apply person-service --local && exec npx wrangler dev --local --ip 0.0.0.0 --port 8787"]
