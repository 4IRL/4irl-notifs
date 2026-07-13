# web — 4IRL Notifications Admin UI

React + Vite SPA for managing apps, users, and their notification associations via the
`provisioning-api`. Deployed to Cloudflare Pages (build: `npm run build` → `dist/`), gated by
Cloudflare Access in production.

## Commands

| Command                  | Description                                           |
| ------------------------ | ----------------------------------------------------- |
| `npm run dev`            | Vite dev server                                       |
| `npm test`               | Unit/component tests (Vitest + React Testing Library) |
| `npx playwright test`    | E2E tests (Playwright)                                |
| `npm run build`          | Type-check (`tsc -b`) + production build              |
| `npx eslint .`           | Lint                                                  |
| `npx prettier --check .` | Formatting check                                      |
