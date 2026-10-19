import { NTFY_SERVER } from './params';

/** ntfy app listing on the Apple App Store. */
export const APP_STORE_URL = 'https://apps.apple.com/app/ntfy/id1625396347';

/** ntfy app listing on Google Play. */
export const GOOGLE_PLAY_URL = 'https://play.google.com/store/apps/details?id=io.heckel.ntfy';

const HTTPS_PREFIX = 'https://';

/** The one-tap `ntfy://` deep link that subscribes the Android app to a topic. */
export function subscribeHref(topic: string): string {
  return `ntfy://${NTFY_SERVER.slice(HTTPS_PREFIX.length)}/${topic}`;
}
