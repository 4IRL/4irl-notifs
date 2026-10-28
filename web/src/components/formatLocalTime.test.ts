import { describe, expect, it } from 'vitest';
import {
  formatDateTimeZone,
  formatTimeOfDay,
  localZoneName,
  toIsoInstant,
} from './formatLocalTime';

const LA = 'America/Los_Angeles';
const KOLKATA = 'Asia/Kolkata';

describe('test environment pin', () => {
  it('runs the Vitest worker in UTC', () => {
    expect(new Date(0).getTimezoneOffset()).toBe(0);
    expect(Intl.DateTimeFormat().resolvedOptions().timeZone).toBe('UTC');
  });
});

describe('formatDateTimeZone', () => {
  it('appends the zone abbreviation', () => {
    const timestamp = '2026-10-18T14:05:00Z';
    expect(formatDateTimeZone({ timestamp, timeZone: 'UTC' })).toBe('2026-10-18 14:05 UTC');
    expect(formatDateTimeZone({ timestamp, timeZone: LA })).toBe('2026-10-18 07:05 PDT');
    expect(formatDateTimeZone({ timestamp: '2026-12-18T14:05:00Z', timeZone: LA })).toBe(
      '2026-12-18 06:05 PST',
    );
    expect(formatDateTimeZone({ timestamp, timeZone: KOLKATA })).toBe('2026-10-18 19:35 GMT+5:30');
  });

  it('defaults to the browser zone (UTC under the test pin)', () => {
    expect(formatDateTimeZone({ timestamp: '2026-10-17T08:12:44Z' })).toBe('2026-10-17 08:12 UTC');
  });

  it('rolls the date over across midnight', () => {
    expect(formatDateTimeZone({ timestamp: '2026-10-17T20:00:00Z', timeZone: KOLKATA })).toBe(
      '2026-10-18 01:30 GMT+5:30',
    );
    expect(formatDateTimeZone({ timestamp: '2026-10-17T02:00:00Z', timeZone: LA })).toBe(
      '2026-10-16 19:00 PDT',
    );
  });

  it('prints midnight as 00:xx, never 24:xx', () => {
    expect(formatDateTimeZone({ timestamp: '2026-10-17T07:05:00Z', timeZone: LA })).toBe(
      '2026-10-17 00:05 PDT',
    );
  });

  it('follows the offset change at a DST boundary', () => {
    expect(formatDateTimeZone({ timestamp: '2026-11-01T08:59:00Z', timeZone: LA })).toBe(
      '2026-11-01 01:59 PDT',
    );
    expect(formatDateTimeZone({ timestamp: '2026-11-01T09:00:00Z', timeZone: LA })).toBe(
      '2026-11-01 01:00 PST',
    );
  });

  it('reads offset-less timestamps as UTC and honours explicit offsets', () => {
    expect(formatDateTimeZone({ timestamp: '2026-10-17T08:12:44', timeZone: LA })).toBe(
      '2026-10-17 01:12 PDT',
    );
    expect(formatDateTimeZone({ timestamp: '2026-10-17 08:12', timeZone: LA })).toBe(
      '2026-10-17 01:12 PDT',
    );
    expect(formatDateTimeZone({ timestamp: '2026-10-17T08:12:00+05:30', timeZone: 'UTC' })).toBe(
      '2026-10-17 02:42 UTC',
    );
  });

  it.each([
    ['+05:30', '2026-10-17T08:12:00+05:30'],
    ['+0530 (no colon)', '2026-10-17T08:12:00+0530'],
    ['+05:30 after a space', '2026-10-17 08:12:00 +05:30'],
    ['hour-only +05', '2026-10-17T07:42:00+05'],
  ])('normalises the offset form %s to the same UTC instant', (_label, timestamp) => {
    expect(formatTimeOfDay({ timestamp, timeZone: 'UTC' })).toBe('02:42');
  });

  it('treats a lowercase z as UTC and a date-only value as UTC midnight', () => {
    expect(formatDateTimeZone({ timestamp: '2026-10-17T08:12:44z', timeZone: 'UTC' })).toBe(
      '2026-10-17 08:12 UTC',
    );
    expect(formatDateTimeZone({ timestamp: '2026-10-17', timeZone: LA })).toBe(
      '2026-10-16 17:00 PDT',
    );
  });

  it('accepts fractional seconds and trims surrounding whitespace', () => {
    expect(toIsoInstant({ timestamp: '2026-10-17T08:12:44.123456789Z' })).toBe(
      '2026-10-17T08:12:44.123Z',
    );
    expect(toIsoInstant({ timestamp: '  2026-10-17T08:12:44Z  ' })).toBe(
      '2026-10-17T08:12:44.000Z',
    );
  });

  it('returns unparseable input unchanged', () => {
    expect(formatDateTimeZone({ timestamp: 'soon', timeZone: 'UTC' })).toBe('soon');
    expect(formatDateTimeZone({ timestamp: '', timeZone: 'UTC' })).toBe('');
  });
});

describe('formatTimeOfDay', () => {
  it('prints HH:mm by default and HH:mm:ss with withSeconds', () => {
    const timestamp = '2026-10-17T08:12:44Z';
    expect(formatTimeOfDay({ timestamp, timeZone: 'UTC' })).toBe('08:12');
    expect(formatTimeOfDay({ timestamp, timeZone: 'UTC', withSeconds: true })).toBe('08:12:44');
    expect(formatTimeOfDay({ timestamp, timeZone: KOLKATA, withSeconds: true })).toBe('13:42:44');
  });

  it('prints midnight as 00:xx', () => {
    expect(
      formatTimeOfDay({ timestamp: '2026-10-17T07:00:09Z', timeZone: LA, withSeconds: true }),
    ).toBe('00:00:09');
  });

  it('returns unparseable input unchanged', () => {
    expect(formatTimeOfDay({ timestamp: 'nope', timeZone: 'UTC' })).toBe('nope');
  });
});

describe('localZoneName', () => {
  it('returns the injected IANA zone', () => {
    expect(localZoneName({ timeZone: LA })).toBe('America/Los_Angeles');
    expect(localZoneName({ timeZone: KOLKATA })).toMatch(/^Asia\/(Kolkata|Calcutta)$/);
  });

  it('defaults to the browser zone', () => {
    expect(localZoneName()).toBe('UTC');
  });
});
