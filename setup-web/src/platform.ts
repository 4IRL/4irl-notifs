export type Platform = 'ios' | 'android';

const IOS_PATTERN = /iPhone|iPad|iPod/;
const MACINTOSH_PATTERN = /Macintosh/;
const ANDROID_PATTERN = /Android/;

/**
 * Detects the visitor's phone platform from the user agent. iPadOS reports a
 * desktop-class "Macintosh" UA, so a Macintosh UA with more than one touch
 * point is treated as iOS. Anything else (desktop) returns null.
 */
export function detectPlatform({
  userAgent,
  maxTouchPoints,
}: {
  userAgent: string;
  maxTouchPoints: number;
}): Platform | null {
  if (IOS_PATTERN.test(userAgent)) {
    return 'ios';
  }
  if (MACINTOSH_PATTERN.test(userAgent) && maxTouchPoints > 1) {
    return 'ios';
  }
  if (ANDROID_PATTERN.test(userAgent)) {
    return 'android';
  }
  return null;
}
