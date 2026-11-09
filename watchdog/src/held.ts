import type { HeldApp, HeldSnapshot } from './types';

function asRecord(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function isCount(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0;
}

/** Reads `{count}` from an optional group: absent => 0, present but malformed => null. */
function readGroupCount(group: unknown): number | null {
  if (group === undefined) {
    return 0;
  }
  const record = asRecord(group);
  if (record === null || !isCount(record.count)) {
    return null;
  }
  return record.count;
}

function readApps(value: unknown): HeldApp[] | null {
  if (value === undefined) {
    return [];
  }
  if (!Array.isArray(value)) {
    return null;
  }
  const apps: HeldApp[] = [];
  for (const entry of value) {
    const record = asRecord(entry);
    if (record === null || typeof record.app_id !== 'string' || record.app_id === '' || !isCount(record.count)) {
      return null;
    }
    apps.push({ appId: record.app_id, count: record.count });
  }
  return apps;
}

/**
 * Validates delivery-api's `/readyz` `held` block. Fails closed: returns null (held state left
 * untouched) for an unavailable block or any malformed field, never zero counts. `total` and
 * `as_of` are ignored; the total is recomputed from the parts.
 */
export function parseHeldBlock(value: unknown): HeldSnapshot | null {
  const block = asRecord(value);
  if (block === null || block.available !== true) {
    return null;
  }
  if (block.stale !== undefined && typeof block.stale !== 'boolean') {
    return null;
  }
  const messages = readGroupCount(block.messages);
  const unassigned = readGroupCount(block.unassigned_alerts);
  const apps = readApps(block.apps);
  if (messages === null || unassigned === null || apps === null) {
    return null;
  }
  let oldestAgeSeconds: number | null = null;
  const messagesGroup = asRecord(block.messages);
  const rawAge = messagesGroup?.oldest_age_seconds;
  if (rawAge !== undefined && rawAge !== null) {
    if (!isCount(rawAge)) {
      return null;
    }
    oldestAgeSeconds = rawAge;
  }
  const rawMore = block.apps_more;
  if (rawMore !== undefined && !isCount(rawMore)) {
    return null;
  }
  return {
    stale: block.stale === true,
    messages,
    oldestAgeSeconds,
    unassigned,
    apps,
    appsMore: rawMore ?? 0,
  };
}
