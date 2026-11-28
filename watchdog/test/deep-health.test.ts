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

async function detailFor({ body, status = 200 }: { body: unknown; status?: number }): Promise<string | null> {
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
    expect(await detailFor({ body: { status: 'Success', components: { db: 'up', cache: 'up' } } })).toBeNull();
  });

  it('judges only the required components the target declares, not urls4irl ones', async () => {
    expect(await detailFor({ body: { status: 'Success', components: { db: 'up' } } })).toBeNull();
    expect(await detailFor({ body: { status: 'Success', components: {} } })).toBe('unhealthy: db=missing');
  });

  it('lists required components first and once, then extra reported ones in body order', async () => {
    const components = { queue: 'down', db: 'not configured', cache: 'down' };
    expect(await detailFor({ body: { status: 'Success', components } })).toBe(
      'unhealthy: db=not configured, queue=down, cache=down',
    );
  });

  it('shows an extra component with an unknown value as invalid', async () => {
    const components = { db: 'up', cache: 'degraded', queue: 5 };
    expect(await detailFor({ body: { status: 'Success', components } })).toBe(
      'unhealthy: cache=invalid, queue=invalid',
    );
  });

  it('names an unhealthy extra component in a 503 body', async () => {
    const body = { status: 'Failure', message: 'x', components: { db: 'up', cache: 'down' } };
    expect(await detailFor({ body, status: 503 })).toBe('unhealthy: cache=down');
  });

  it('names the target secret in the 401 and 429 details', async () => {
    expect(await detailFor({ body: { status: 'Failure', message: 'Unauthorized' }, status: 401 })).toBe(
      'unauthorized: check HEALTH_TOKEN_URLS4IRL',
    );
    expect(await detailFor({ body: {}, status: 429 })).toBe('rate limited (HTTP 429): check HEALTH_TOKEN_URLS4IRL');
  });

  it('never echoes a reported component key that is not a plain snake_case name', async () => {
    const components = { db: 'up', 'Bad Key <@everyone>': 'down', ['x'.repeat(41)]: 'down' };
    expect(await detailFor({ body: { status: 'Success', components } })).toBe('unhealthy: unnamed=down, unnamed=down');
  });

  /** `db` plus `extras` extra components, all down. */
  function allDown(extras: number): Record<string, string> {
    const components: Record<string, string> = { db: 'down' };
    for (let index = 1; index <= extras; index += 1) {
      components[`extra_${index}`] = 'down';
    }
    return components;
  }

  /** The detail parts for `db` and `extra_1..extra_<last>`. */
  function parts(last: number): string {
    return ['db=down', ...Array.from({ length: last }, (_, index) => `extra_${index + 1}=down`)].join(', ');
  }

  it(`lists exactly ${DEEP_HEALTH_COMPONENTS_SHOWN} components without a count`, async () => {
    const components = allDown(DEEP_HEALTH_COMPONENTS_SHOWN - 1);
    expect(await detailFor({ body: { status: 'Success', components } })).toBe(
      `unhealthy: ${parts(DEEP_HEALTH_COMPONENTS_SHOWN - 1)}`,
    );
  });

  it(`caps the listed components at ${DEEP_HEALTH_COMPONENTS_SHOWN} and counts the rest`, async () => {
    const shown = `unhealthy: ${parts(DEEP_HEALTH_COMPONENTS_SHOWN - 1)}`;
    expect(await detailFor({ body: { status: 'Success', components: allDown(DEEP_HEALTH_COMPONENTS_SHOWN) } })).toBe(
      `${shown}, +1 more`,
    );
    expect(
      await detailFor({ body: { status: 'Success', components: allDown(DEEP_HEALTH_COMPONENTS_SHOWN + 2) } }),
    ).toBe(`${shown}, +3 more`);
  });

  it('gives each token-protected target its own secret', () => {
    const secrets = TARGETS.flatMap((target) => (target.tokenSecret === undefined ? [] : [target.tokenSecret]));
    expect(new Set(secrets).size).toBe(secrets.length);
    expect(TARGETS.find((target) => target.id === 'urls4irl')?.tokenSecret).toBe('HEALTH_TOKEN_URLS4IRL');
  });
});
