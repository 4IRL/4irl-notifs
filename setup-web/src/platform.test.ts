import { describe, expect, it } from 'vitest';

import { detectPlatform } from './platform';

const IPHONE_UA =
  'Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1';
const IPAD_UA =
  'Mozilla/5.0 (iPad; CPU OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1';
const IPOD_UA =
  'Mozilla/5.0 (iPod touch; CPU iPhone OS 15_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/15.0 Mobile/15E148 Safari/604.1';
const MACOS_UA =
  'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15';
const ANDROID_PHONE_UA =
  'Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36';
const ANDROID_TABLET_UA =
  'Mozilla/5.0 (Linux; Android 13; SM-X700) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36';
const WINDOWS_UA =
  'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36';
const LINUX_UA =
  'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36';

describe('detectPlatform', () => {
  it.each([
    ['iPhone', IPHONE_UA, 5, 'ios'],
    ['iPad', IPAD_UA, 5, 'ios'],
    ['iPod', IPOD_UA, 5, 'ios'],
    ['iPadOS desktop-class UA with touch', MACOS_UA, 5, 'ios'],
    ['iPadOS desktop-class UA with exactly 2 touch points', MACOS_UA, 2, 'ios'],
    ['Android phone', ANDROID_PHONE_UA, 5, 'android'],
    ['Android tablet', ANDROID_TABLET_UA, 5, 'android'],
    ['macOS without touch', MACOS_UA, 0, null],
    ['macOS with a single touch point', MACOS_UA, 1, null],
    ['Windows desktop', WINDOWS_UA, 0, null],
    ['Windows touch laptop', WINDOWS_UA, 10, null],
    ['Linux desktop', LINUX_UA, 0, null],
    ['empty UA', '', 0, null],
  ] as const)('detects %s', (_label, userAgent, maxTouchPoints, expected) => {
    expect(detectPlatform({ userAgent, maxTouchPoints })).toBe(expected);
  });
});
