/** Bindings of the watchdog Worker. The three secrets are optional so a missing one is detectable at runtime. */
export interface Env {
  WATCHDOG_STATE: KVNamespace;
  DISCORD_WEBHOOK_URL?: string;
  CF_ACCESS_CLIENT_ID?: string;
  CF_ACCESS_CLIENT_SECRET?: string;
  /** Pause before the single retry of a failed probe, in ms. Unset, empty or invalid means 2000; tests set 0. */
  PROBE_RETRY_DELAY_MS?: string;
}

export type TargetStatus = 'up' | 'down';

/** Last-known state of one probe target. */
export interface TargetState {
  status: TargetStatus;
  /** ISO timestamp of the last status change (or of the first observation). */
  since: string;
  /** Failure detail while down; null while up. */
  detail: string | null;
}

/** The single KV document stored under STATE_KEY. */
export interface WatchdogState {
  version: 1;
  targets: Record<string, TargetState>;
  /** UTC date (YYYY-MM-DD) of the last daily summary that was posted. */
  last_summary_date: string | null;
}

export interface ProbeVerdict {
  ok: boolean;
  /** Human-readable failure detail; null when ok. */
  detail: string | null;
}

/** One external health probe. `evaluate` judges a response that already passed the redirect/auth checks. */
export interface Target {
  id: string;
  url: string;
  /** Whether the Cloudflare Access service-token headers are sent to this target. */
  access: boolean;
  evaluate: ({ response, now }: { response: Response; now: number }) => Promise<ProbeVerdict>;
}

export interface ProbeResult {
  id: string;
  verdict: ProbeVerdict;
  /** HTTP status of the response, or null when none arrived (network error, or a timeout before the headers). */
  httpStatus: number | null;
  durationMs: number;
}
