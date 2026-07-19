import { applyD1Migrations, env } from 'cloudflare:test';

// Runs once per worker isolate before the test file's own tests execute,
// applying the migrations/ SQL files to the in-memory miniflare D1 binding
// so every test starts against a fresh, migrated `person` table.
await applyD1Migrations(env.DB, env.TEST_MIGRATIONS);
