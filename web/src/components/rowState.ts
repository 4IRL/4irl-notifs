import { ApiError } from '../api/client';

/** The server's message for an ApiError rejection, otherwise `fallback`. */
export function messageOf({
  rejection,
  fallback,
}: {
  rejection: unknown;
  fallback: string;
}): string {
  return rejection instanceof ApiError ? rejection.message : fallback;
}

/** A copy of `record` without `key`. */
export function withoutKey<K extends string | number, T>({
  record,
  key,
}: {
  record: Record<K, T>;
  key: K;
}): Record<K, T> {
  const next = { ...record };
  delete next[key];
  return next;
}
