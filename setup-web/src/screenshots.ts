// Typed manifest of the page's own screenshots. They are first-party, same-origin
// assets served from /screenshots/ (setup-web/public/screenshots/). Every entry
// points at its final path, and `screenshots.test.ts` gates each file's existence,
// size (<= 200 KB) and real PNG dimensions against the width/height recorded here,
// so the <img> reserves layout space (no shift when the image loads).
//
// All five images are real phone captures (four iPhone, one Android) with the
// status bar (and, on Android, the on-screen keyboard) cropped off, metadata
// stripped and resized to 720px wide.

export interface ScreenshotAsset {
  src: string;
  alt: string;
  width: number;
  height: number;
}

export type ScreenshotKey =
  'iosDefaultServer' | 'iosAddUser' | 'iosSubscribeButton' | 'iosSubscribeTopic' | 'androidAddUser';

export const screenshots: Record<ScreenshotKey, ScreenshotAsset> = {
  iosDefaultServer: {
    src: '/screenshots/ios-default-server.png',
    alt: 'ntfy Default server screen on iPhone with the server field set to https://notifs.4irl.app',
    width: 720,
    height: 457,
  },
  iosAddUser: {
    src: '/screenshots/ios-add-user.png',
    alt: 'ntfy Add user screen on iPhone with the server address, Username and Password fields',
    width: 720,
    height: 557,
  },
  iosSubscribeButton: {
    src: '/screenshots/ios-subscribe-button.png',
    alt: 'ntfy topic list on iPhone with the + button at the top right',
    width: 720,
    height: 306,
  },
  iosSubscribeTopic: {
    src: '/screenshots/ios-subscribe-topic.png',
    alt: 'ntfy Add subscription screen on iPhone with the Topic name field and Use another server switched off',
    width: 720,
    height: 541,
  },
  androidAddUser: {
    src: '/screenshots/android-add-user.png',
    alt: 'ntfy Add user screen on Android with the Service URL, Username and Password fields',
    width: 720,
    height: 648,
  },
};
