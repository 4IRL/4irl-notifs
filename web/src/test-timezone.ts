import { afterAll, beforeAll, vi } from 'vitest';

/**
 * Runs the enclosing describe block under `zone` (an IANA name) and restores the
 * suite-wide UTC pin afterwards. Node re-reads process.env.TZ on assignment, so
 * both Date and Intl follow it. Call at describe scope.
 */
export function useTimeZone(zone: string): void {
  beforeAll(() => {
    vi.stubEnv('TZ', zone);
  });
  afterAll(() => {
    vi.unstubAllEnvs();
  });
}
