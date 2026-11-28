import { describe, expect, it } from 'vitest';
import { DEEP_HEALTH_COMPONENTS_SHOWN, TARGETS, deepHealthTarget } from '../src/targets';

const NOW = Date.UTC(2026, 10, 27, 10, 0, 0);

/** A second, hypothetical app: proves the evaluator holds no urls4irl-specific component or secret. */
const otherApp = deepHealthTarget({
  id: 'other-app',
  url: 'https://other.4irl.app/health/deep',
  tokenSecret: 'HEALTH_TOKEN_URLS4IRL',
  requiredComponents: ['db'],
});

async function detailFor(body: unknown, status = 200): Promise<string | null> {
  const verdict = await otherApp.evaluate({ response: Response.json(body, { status }), now: NOW });
  return verdict.ok ? null : verdict.detail;
}

describe('deepHealthTarget', () => {
  it('builds a non-Access target that sends the named token secret', () => {
    expect(otherApp).toMatchObject({
      id: 'other-app',
      url: 'https://other.4irl.app/health/deep',
      access: false,
      tokenSecret: 'HEALTH_TOKEN_URLS4IRL',
    });
  });

  it('is up when every required and every reported component is up', async () => {
    expect(await detailFor({ status: 'Success', components: { db: 'up', cache: 'up' } })).toBeNull();
  });

  it('judges only the required components the target declares, not urls4irl ones', async () => {
    expect(await detailFor({ status: 'Success', components: { db: 'up' } })).toBeNull();
    expect(await detailFor({ status: 'Success', components: {} })).toBe('unhealthy: db=missing');
  });

  it('lists required components first, then extra reported ones in body order', async () => {
    const components = { queue: 'down', db: 'not configured', cache: 'down' };
    expect(await detailFor({ status: 'Success', components })).toBe(
      'unhealthy: db=not configured, queue=down, cache=down',
    );
  });

  it('names the target secret in the 401 and 429 details', async () => {
    expect(await detailFor({ status: 'Failure', message: 'Unauthorized' }, 401)).toBe(
      'unauthorized: check HEALTH_TOKEN_URLS4IRL',
    );
    expect(await detailFor({}, 429)).toBe('rate limited (HTTP 429): check HEALTH_TOKEN_URLS4IRL');
  });

  it('never echoes a reported component key that is not a plain snake_case name', async () => {
    const components = { db: 'up', 'Bad Key <@everyone>': 'down', ['x'.repeat(41)]: 'down' };
    expect(await detailFor({ status: 'Success', components })).toBe('unhealthy: unnamed=down, unnamed=down');
  });

  it(`caps the listed components at ${DEEP_HEALTH_COMPONENTS_SHOWN} and counts the rest`, async () => {
    const components: Record<string, string> = { db: 'down' };
    for (let index = 1; index <= DEEP_HEALTH_COMPONENTS_SHOWN + 2; index += 1) {
      components[`extra_${index}`] = 'down';
    }
    const detail = await detailFor({ status: 'Success', components });
    expect(detail?.split(', ')).toHaveLength(DEEP_HEALTH_COMPONENTS_SHOWN + 1);
    expect(detail?.endsWith(', +3 more')).toBe(true);
  });

  it('gives each token-protected target its own secret', () => {
    const secrets = TARGETS.flatMap((target) => (target.tokenSecret === undefined ? [] : [target.tokenSecret]));
    expect(new Set(secrets).size).toBe(secrets.length);
    expect(TARGETS.find((target) => target.id === 'urls4irl')?.tokenSecret).toBe('HEALTH_TOKEN_URLS4IRL');
  });
});
