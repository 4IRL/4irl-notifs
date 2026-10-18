import { renderHook } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { useLatestRequest } from './useLatestRequest';

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (error: unknown) => void;
}

function defer<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe('useLatestRequest', () => {
  it('reports a lone request as the latest value', async () => {
    const { result } = renderHook(() => useLatestRequest());

    await expect(result.current(Promise.resolve('a'))).resolves.toEqual({
      stale: false,
      ok: true,
      value: 'a',
    });
  });

  it('ignores an earlier request that resolves after a later one started', async () => {
    const { result } = renderHook(() => useLatestRequest());
    const first = defer<string>();
    const second = defer<string>();

    const firstOutcome = result.current(first.promise);
    const secondOutcome = result.current(second.promise);
    second.resolve('second');
    first.resolve('first');

    await expect(secondOutcome).resolves.toEqual({ stale: false, ok: true, value: 'second' });
    await expect(firstOutcome).resolves.toEqual({ stale: true });
  });

  it('ignores an earlier request that resolves before a later one settles', async () => {
    const { result } = renderHook(() => useLatestRequest());
    const first = defer<string>();
    const second = defer<string>();

    const firstOutcome = result.current(first.promise);
    const secondOutcome = result.current(second.promise);
    first.resolve('first');
    await expect(firstOutcome).resolves.toEqual({ stale: true });
    second.resolve('second');

    await expect(secondOutcome).resolves.toEqual({ stale: false, ok: true, value: 'second' });
  });

  it('drops a superseded rejection without surfacing the error', async () => {
    const { result } = renderHook(() => useLatestRequest());
    const first = defer<string>();
    const second = defer<string>();

    const firstOutcome = result.current(first.promise);
    const secondOutcome = result.current(second.promise);
    first.reject(new Error('boom'));
    second.resolve('second');

    await expect(firstOutcome).resolves.toEqual({ stale: true });
    await expect(secondOutcome).resolves.toEqual({ stale: false, ok: true, value: 'second' });
  });

  it('surfaces the latest rejection as a non-stale failure', async () => {
    const { result } = renderHook(() => useLatestRequest());
    const failure = new Error('boom');

    await expect(result.current(Promise.reject(failure))).resolves.toEqual({
      stale: false,
      ok: false,
      error: failure,
    });
  });

  it('keeps a stable function identity across renders', () => {
    const { result, rerender } = renderHook(() => useLatestRequest());
    const first = result.current;

    rerender();

    expect(result.current).toBe(first);
  });
});
