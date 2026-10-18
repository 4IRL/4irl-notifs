import { useCallback, useRef } from 'react';

/** Settled outcome of a tracked request. `stale` means a newer request was
 *  started after this one, so the caller must ignore the result entirely. */
export type LatestResult<T> =
  | { stale: true }
  | { stale: false; ok: true; value: T }
  | { stale: false; ok: false; error: unknown };

/**
 * Shared stale-response guard for overlapping async refreshes. Every call to
 * the returned `track` function takes the next monotonically increasing token;
 * when the tracked promise settles, the result reports `stale: true` unless
 * that token is still the latest. The returned promise never rejects, so a
 * superseded rejection is silently dropped and the latest one is surfaced as
 * `{ ok: false }`. The function identity is stable across renders.
 */
export function useLatestRequest(): <T>(request: Promise<T>) => Promise<LatestResult<T>> {
  const latestToken = useRef(0);

  return useCallback(<T>(request: Promise<T>): Promise<LatestResult<T>> => {
    latestToken.current += 1;
    const token = latestToken.current;
    return request.then(
      (value): LatestResult<T> =>
        token === latestToken.current ? { stale: false, ok: true, value } : { stale: true },
      (error: unknown): LatestResult<T> =>
        token === latestToken.current ? { stale: false, ok: false, error } : { stale: true },
    );
  }, []);
}
