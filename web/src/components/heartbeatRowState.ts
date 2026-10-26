import type { HeartbeatCheck } from '../api/deliveryClient';
import { strings } from '../strings';

/** What a row shows; `late` is derived client-side and never stored or alerted on. */
export type RowState = 'up' | 'late' | 'down' | 'paused' | 'waiting';

export const TICK_MS = 30_000;
const MS_PER_SECOND = 1000;

export function isPastDeadline({ check, now }: { check: HeartbeatCheck; now: number }): boolean {
  return now > Date.parse(check.deadlineAt);
}

/** The state a check would show if it were not paused. */
export function liveStateOf({ check, now }: { check: HeartbeatCheck; now: number }): RowState {
  if (check.probe !== null) {
    // A probe never reads Late; `deadlineAt`/`graceSeconds` are derived backstops.
    switch (check.state) {
      case 'new':
        return 'waiting';
      case 'up':
        return 'up';
      default:
        // `down`, and fail closed on any other value (including an unexpected `late`).
        return 'down';
    }
  }
  switch (check.state) {
    case 'new':
      return 'waiting';
    case 'up':
    case 'late': {
      // The late boundary is anchor + period = deadline - grace. The server's
      // own `late` is never hidden, so a skewed browser clock cannot mask it.
      const lateAfter = Date.parse(check.deadlineAt) - check.graceSeconds * MS_PER_SECOND;
      return check.state === 'late' || now > lateAfter ? 'late' : 'up';
    }
    default:
      // `down`, and fail closed on any state this build does not know.
      return 'down';
  }
}

export function rowStateOf({ check, now }: { check: HeartbeatCheck; now: number }): RowState {
  return check.paused ? 'paused' : liveStateOf({ check, now });
}

/**
 * True when the server reports `up` but the local clock says the check has
 * crossed its late boundary: the server will by now report it `late`, so one
 * reload brings the list back in line. A server-reported `late` never counts
 * (the server already knows), and a paused check is never late. A probe is
 * never late.
 */
export function turnedLateLocally({ check, now }: { check: HeartbeatCheck; now: number }): boolean {
  if (check.probe !== null) {
    return false;
  }
  return !check.paused && check.state === 'up' && liveStateOf({ check, now }) === 'late';
}

/** Identifies one lateness episode: a ping moves `deadline_at`, so it starts a new one. */
export function lateKeyOf(check: HeartbeatCheck): string {
  return `${check.id}:${check.deadlineAt}`;
}

/**
 * True once a null-`lastResult` observation window has run out: one interval
 * plus one tick of slack (a resume or edit resets `last_result`, and the next
 * result can land up to ~18 s later).
 */
export function nullWindowExpired({
  nullSeenAt,
  intervalSeconds,
  now,
}: {
  nullSeenAt: number;
  intervalSeconds: number;
  now: number;
}): boolean {
  return now > nullSeenAt + intervalSeconds * MS_PER_SECOND + TICK_MS;
}

/**
 * True when the server may have moved a live check on since the last load: it
 * is Down (it recovers on the server without the list knowing) or past its
 * deadline (about to go, or just gone, Down). The caller's own Late-once rule
 * and the in-flight and visibility guards bound how often this reloads.
 *
 * A probe ignores the deadline: it reloads when Down, when its last result is
 * stale or unparsable, or when a null last result has been seen for a full
 * window (`nullSeenAt`, tracked by the caller).
 */
export function needsReload({
  check,
  now,
  nullSeenAt,
}: {
  check: HeartbeatCheck;
  now: number;
  nullSeenAt?: number;
}): boolean {
  if (check.paused) {
    return false;
  }
  const { probe } = check;
  if (probe !== null) {
    if (check.state === 'down') {
      return true;
    }
    if (probe.lastResult === null) {
      return (
        nullSeenAt !== undefined &&
        nullWindowExpired({ nullSeenAt, intervalSeconds: probe.intervalSeconds, now })
      );
    }
    const resultAt = Date.parse(probe.lastResult.at);
    return Number.isNaN(resultAt) || now > resultAt + probe.intervalSeconds * MS_PER_SECOND;
  }
  return liveStateOf({ check, now }) === 'down' || isPastDeadline({ check, now });
}

export function wasLabel(state: RowState): string {
  switch (state) {
    case 'up':
      return strings.heartbeatWasUp;
    case 'late':
      return strings.heartbeatWasLate;
    case 'waiting':
      return strings.heartbeatWasWaiting;
    default:
      return strings.heartbeatWasDown;
  }
}

const HTTP_FORBIDDEN = 403;

/**
 * A 403 from the probed endpoint may mean a Cloudflare Access edge rejection (an expired or
 * revoked token, or no token at all). The stored-credential `config` reason is a different
 * failure and never triggers this.
 */
export function probeAccessHintOf(check: HeartbeatCheck): 'tokenRejected' | 'tokenMissing' | null {
  const { probe } = check;
  if (check.paused || probe === null || probe.lastResult === null) {
    return null;
  }
  const { reason, status } = probe.lastResult;
  if (reason !== 'http_status' || status !== HTTP_FORBIDDEN) {
    return null;
  }
  return probe.hasAccessToken ? 'tokenRejected' : 'tokenMissing';
}

/** What a paused probe was doing: `null` renders plain Paused with no "was" line. */
export function probePausedWasOf(check: HeartbeatCheck): 'up' | 'down' | null {
  switch (check.state) {
    case 'up':
      return 'up';
    case 'down':
      return 'down';
    default:
      return null;
  }
}
