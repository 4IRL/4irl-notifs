import { act, renderHook } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { useExclusive } from './useExclusive';

function defer<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

describe('useExclusive', () => {
  it('marks a key busy while its work is in flight and clears it after', async () => {
    const { result } = renderHook(() => useExclusive<string>());
    const gate = defer<string>();

    let running!: Promise<string>;
    act(() => {
      running = result.current.exclusive({ key: 'a', work: () => gate.promise });
    });
    expect(result.current.isBusy('a')).toBe(true);
    expect(result.current.isBusy('b')).toBe(false);

    await act(async () => {
      gate.resolve('done');
      await running;
    });
    expect(result.current.isBusy('a')).toBe(false);
  });

  it('rejects a second run of the same key while the first is in flight', async () => {
    const { result } = renderHook(() => useExclusive<string>());
    const gate = defer<string>();

    let first!: Promise<string>;
    let second!: Promise<string>;
    act(() => {
      first = result.current.exclusive({ key: 'a', work: () => gate.promise });
      second = result.current.exclusive({ key: 'a', work: () => Promise.resolve('x') });
    });
    await expect(second).rejects.toThrow('action already in flight');

    await act(async () => {
      gate.resolve('done');
      await first;
    });
  });

  it('allows different keys concurrently and re-running after completion', async () => {
    const { result } = renderHook(() => useExclusive<string>());

    let outcomes!: string[];
    await act(async () => {
      outcomes = await Promise.all([
        result.current.exclusive({ key: 'a', work: () => Promise.resolve('a') }),
        result.current.exclusive({ key: 'b', work: () => Promise.resolve('b') }),
      ]);
    });
    expect(outcomes).toEqual(['a', 'b']);

    await act(async () => {
      await expect(
        result.current.exclusive({ key: 'a', work: () => Promise.resolve('again') }),
      ).resolves.toBe('again');
    });
  });

  it('clears the busy flag when the work rejects', async () => {
    const { result } = renderHook(() => useExclusive<string>());

    await act(async () => {
      await expect(
        result.current.exclusive({ key: 'a', work: () => Promise.reject(new Error('boom')) }),
      ).rejects.toThrow('boom');
    });
    expect(result.current.isBusy('a')).toBe(false);
  });
});
