import { useRef, useState } from 'react';

import { withoutKey } from '../components/rowState';

export interface Exclusive<K extends string> {
  /** Whether work under `key` is currently in flight. */
  isBusy: (key: K) => boolean;
  /** Runs `work` unless `key` is already in flight, in which case it rejects. */
  exclusive: <T>(args: { key: K; work: () => Promise<T> }) => Promise<T>;
}

/**
 * Per-key in-flight guard. The ref blocks a second synchronous run of the same
 * key before React re-renders; the state mirrors it so the UI can show busy.
 */
export function useExclusive<K extends string>(): Exclusive<K> {
  const [busyKeys, setBusyKeys] = useState<Record<string, true>>({});
  const inFlight = useRef(new Set<K>());

  function isBusy(key: K): boolean {
    return busyKeys[key] === true;
  }

  async function exclusive<T>({ key, work }: { key: K; work: () => Promise<T> }): Promise<T> {
    if (inFlight.current.has(key)) {
      throw new Error('action already in flight');
    }
    inFlight.current.add(key);
    setBusyKeys((current): Record<string, true> => ({ ...current, [key]: true }));
    try {
      return await work();
    } finally {
      inFlight.current.delete(key);
      setBusyKeys((current) => withoutKey({ record: current, key }));
    }
  }

  return { isBusy, exclusive };
}
