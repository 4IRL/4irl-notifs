import { describe, expect, it } from 'vitest';

import { strings } from '../strings';
import { probeResultCopy, reasonCopy } from './heartbeatReasonCopy';

const REASONS = [
  'missed',
  'fail',
  'http_status',
  'timeout',
  'body_mismatch',
  'blocked',
  'tls',
  'scheme',
  'network',
  'config',
] as const;

describe('reasonCopy', () => {
  it('gives distinct non-empty text for every down_reason', () => {
    const texts = REASONS.map((reason) => reasonCopy({ reason, status: 503 }));
    texts.forEach((text) => expect(text.length).toBeGreaterThan(0));
    expect(new Set(texts).size).toBe(REASONS.length);
  });

  it('shows the status for http_status', () => {
    expect(reasonCopy({ reason: 'http_status', status: 503 })).toContain('503');
  });

  it('shows no digits for http_status without a status', () => {
    expect(reasonCopy({ reason: 'http_status', status: null })).not.toMatch(/\d/);
  });

  it.each(['weird', ''])('falls back to the generic line for %j', (reason) => {
    expect(reasonCopy({ reason, status: null })).toBe(strings.probeReasonUnknown);
  });
});

describe('probeResultCopy', () => {
  it('reports success with the status', () => {
    expect(probeResultCopy({ result: { at: 't', status: 200, reason: null } })).toBe(
      'OK · HTTP 200',
    );
  });

  it('reports bare OK without a status', () => {
    expect(probeResultCopy({ result: { at: 't', status: null, reason: null } })).toBe('OK');
  });

  it('never uses a failure word for a success', () => {
    const text = probeResultCopy({ result: { at: 't', status: 200, reason: null } });
    expect(text).not.toMatch(/fail|error|timed|blocked|mismatch/i);
  });

  it('uses the reason copy for a failure', () => {
    expect(probeResultCopy({ result: { at: 't', status: 503, reason: 'http_status' } })).toBe(
      'HTTP 503',
    );
  });
});

describe('probe strings', () => {
  it('contain no ping or late wording', () => {
    const keys = Object.keys(strings).filter(
      (key) =>
        key.startsWith('probe') ||
        [
          'heartbeatStateNew',
          'heartbeatFirstProbe',
          'heartbeatAwaitingProbe',
          'heartbeatProbeProgress',
          'heartbeatProbedAgo',
          'heartbeatProbeTimeout',
          'heartbeatDownReasonSince',
        ].includes(key),
    );
    expect(keys.length).toBeGreaterThan(10);
    for (const key of keys) {
      const value = (strings as Record<string, unknown>)[key];
      const text =
        typeof value === 'function'
          ? (value as (args: Record<string, unknown>) => string)({
              status: 503,
              count: 2,
              threshold: 3,
              relative: '2 min ago',
              timeout: 5,
              reason: 'Timed out',
              time: '08:12',
            })
          : String(value);
      expect(text, key).not.toMatch(/ping|late/i);
    }
  });
});
